package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"

	"ticker/internal/advantage"
	"ticker/internal/config"
)

type Server struct {
	cfg *config.Config
	log *logrus.Logger
	adv advantage.IAdvantage
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

func NewServer(cfg *config.Config, log *logrus.Logger, adv advantage.IAdvantage) *Server {
	return &Server{cfg, log, adv}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /average", s.handleAverage)
	return mux
}

func (s *Server) handleAverage(w http.ResponseWriter, r *http.Request) {
	s.log.WithField("ticker", s.cfg.Ticker).Info("GET /average")

	data, err := s.adv.GetTickerData(r.Context())
	if err != nil {
		s.log.WithError(err).Error("unable to fetch ticker data")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	ticker := NewTickerFromStockData(data, s.cfg.NDays)
	ticker.CalculateAverageClose()

	s.writeJSON(w, http.StatusOK, ticker)
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.log.WithError(err).Error("failed to encode response body")
	}
}
