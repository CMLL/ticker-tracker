package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/bradfitz/gomemcache/memcache"
	"github.com/sirupsen/logrus"

	"ticker/internal/advantage"
	"ticker/internal/config"
)

// How long do we keep the fetched data cached for,
// since advantage uses 25 requests per day, we have to be really long with it.
const cacheTTL = 24 * time.Hour

type ICache interface {
	Get(key string) (*memcache.Item, error)
	Set(item *memcache.Item) error
}

type pinger interface {
	Ping() error
}

var _ pinger = (*memcache.Client)(nil)

type Server struct {
	cfg   *config.Config
	log   *logrus.Logger
	adv   advantage.IAdvantage
	cache ICache
}

// Builds a cache key of the symbol + date so that we can avoid repulling the
// same information over and over.
func buildCacheKey(symbol string, t time.Time) string {
	return symbol + "||" + t.Format(time.DateOnly)
}

type Ticker struct {
	Symbol  string  `json:"symbol"`
	Average float32 `json:"average"`
	RawData []Entry `json:"data"`
}

type Entry struct {
	Date  time.Time `json:"date"`
	Open  float32   `json:"open"`
	High  float32   `json:"high"`
	Low   float32   `json:"low"`
	Close float32   `json:"close"`
}

// Calculates the NDays average close based on the descending order of Date
func (t *Ticker) CalculateAverageClose() {
	if len(t.RawData) == 0 {
		t.Average = 0
		return
	}
	sum := float32(0.0)
	for _, entry := range t.RawData {
		sum += entry.Close
	}
	average := sum / float32(len(t.RawData))
	t.Average = float32(average)
}

// Creates a new Ticker from StockData, it parses the structure from a map to an array for better
// response structure
func NewTickerFromStockData(adv advantage.StockData, days int) Ticker {
	result := Ticker{
		Symbol: adv.Metadata.Symbol,
	}
	points := []Entry{}
	for key, value := range adv.Series {
		day, err := time.Parse("2006-01-02", key)
		if err != nil {
			logrus.Warnf("Unable to parse date for entry %s: %s", key, err)
			continue
		}
		open, err := strconv.ParseFloat(value.Open, 64)
		if err != nil {
			continue
		}
		high, err := strconv.ParseFloat(value.High, 32)
		if err != nil {
			continue
		}
		low, err := strconv.ParseFloat(value.Low, 32)
		if err != nil {
			continue
		}
		cls, err := strconv.ParseFloat(value.Close, 32)
		if err != nil {
			continue
		}
		entry := Entry{
			day,
			float32(open),
			float32(high),
			float32(low),
			float32(cls),
		}
		points = append(points, entry)
	}
	slices.SortFunc(points, func(a, b Entry) int {
		return b.Date.Compare(a.Date)
	})
	if days > 0 && len(points) > days {
		points = points[:days]
	}
	result.RawData = points
	return result
}

func NewServer(cfg *config.Config, log *logrus.Logger, adv advantage.IAdvantage, cache ICache) *Server {
	return &Server{cfg, log, adv, cache}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /average", s.handleAverage)
	mux.HandleFunc("GET /health/live", s.handleLive)
	mux.HandleFunc("GET /health/ready", s.handleReady)
	return mux
}

// Liveness probe
func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Readiness probe. Checks if memcached is up with a ping.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if p, ok := s.cache.(pinger); ok {
		if err := p.Ping(); err != nil {
			s.log.WithError(err).Warn("readiness check failed: memcached unreachable")
			s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleAverage(w http.ResponseWriter, r *http.Request) {
	s.log.WithField("ticker", s.cfg.Ticker).Info("GET /average")

	key := buildCacheKey(s.cfg.Ticker, time.Now())
	data, hit := s.cachedData(key)
	if !hit {
		var err error
		data, err = s.adv.GetTickerData(r.Context())
		if err != nil {
			s.log.WithError(err).Error("unable to fetch ticker data")
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		if len(data.Series) > 0 {
			s.cacheData(key, data)
		}
	}
	s.log.WithFields(logrus.Fields{"key": key, "hit": hit}).Info("ticker data")

	ticker := NewTickerFromStockData(data, s.cfg.NDays)
	ticker.CalculateAverageClose()

	s.writeJSON(w, http.StatusOK, ticker)
}

func (s *Server) cachedData(key string) (advantage.StockData, bool) {
	var zero advantage.StockData

	item, err := s.cache.Get(key)
	if errors.Is(err, memcache.ErrCacheMiss) {
		return zero, false
	}
	if err != nil {
		s.log.WithError(err).WithField("key", key).Warn("cache read failed")
		return zero, false
	}

	var data advantage.StockData
	if err := json.Unmarshal(item.Value, &data); err != nil {
		s.log.WithError(err).WithField("key", key).Error("cached value is not valid json")
		return zero, false
	}
	return data, true
}

func (s *Server) cacheData(key string, data advantage.StockData) {
	encoded, err := json.Marshal(data)
	if err != nil {
		s.log.WithError(err).WithField("key", key).Error("unable to encode value for cache")
		return
	}

	item := &memcache.Item{Key: key, Value: encoded, Expiration: int32(cacheTTL.Seconds())}
	if err := s.cache.Set(item); err != nil {
		s.log.WithError(err).WithFields(logrus.Fields{
			"key":   key,
			"bytes": len(encoded),
		}).Warn("cache write failed")
	}
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.log.WithError(err).Error("failed to encode response body")
	}
}
