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
	count := 0
	for key, value := range adv.Series {
		// Stop passing information beyond the days we want.
		if count == days {
			break
		}
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
		entry := Entry{
			day,
			float32(open),
			float32(high),
			float32(low),
			float32(cls),
		}
		points = append(points, entry)
		count += 1
	}
	slices.SortFunc(points, func(a, b Entry) int {
		return b.Date.Compare(a.Date)
	})
	result.RawData = points
	return result
}

func NewServer(cfg *config.Config, log *logrus.Logger) *Server {
	return &Server{cfg, log}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /average", s.handleAverage)
	return mux
}

func (s *Server) handleAverage(w http.ResponseWriter, r *http.Request) {
	s.log.WithField("ticker", s.cfg.Ticker).Info("GET /average")

	adv := advantage.NewAdvantageClient(s.cfg.APIKey, s.cfg.Ticker, s.cfg.NDays)
	data, err := adv.GetTickerData(r.Context())
	if err != nil {
		logrus.Errorf("%s", err)
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
