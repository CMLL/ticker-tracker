package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"ticker/internal/advantage"
	"ticker/internal/config"
)

// fakeAdvantage stands in for AdvantageClient so no test touches the network.
type fakeAdvantage struct {
	data   advantage.StockData
	err    error
	calls  int
	gotCtx context.Context
}

func (f *fakeAdvantage) GetTickerData(ctx context.Context) (advantage.StockData, error) {
	f.calls++
	f.gotCtx = ctx
	return f.data, f.err
}

// Compile-time proof the fake and the real client satisfy the same interface.
var (
	_ advantage.IAdvantage = (*fakeAdvantage)(nil)
	_ advantage.IAdvantage = (*advantage.AdvantageClient)(nil)
)

// Map iteration order is randomised, so ordering and truncation assertions run
// repeatedly: a single pass can pass by luck.
const shuffleRuns = 20

func newTestServer(t *testing.T, adv advantage.IAdvantage, nDays int) *Server {
	t.Helper()
	log := logrus.New()
	log.Out = io.Discard
	cfg := &config.Config{Ticker: "IBM", APIKey: "test-key", NDays: nDays}
	return NewServer(cfg, log, adv)
}

// serie builds one row the way Alpha Vantage sends it — every number a string.
func serie(open, high, low, close string) advantage.TimeSerie {
	return advantage.TimeSerie{Open: open, High: high, Low: low, Close: close}
}

// closeOnly is shorthand for rows where only the close price matters.
func closeOnly(close string) advantage.TimeSerie {
	return serie("1", "1", "1", close)
}

func stockData(symbol string, rows map[string]advantage.TimeSerie) advantage.StockData {
	return advantage.StockData{
		Metadata: advantage.Metadata{Symbol: symbol},
		Series:   rows,
	}
}

func doGet(t *testing.T, s *Server, method string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest(method, "/average", nil))
	return rec
}

func decodeTicker(t *testing.T, rec *httptest.ResponseRecorder) Ticker {
	t.Helper()
	var got Ticker
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response body %q: %v", rec.Body.String(), err)
	}
	return got
}

func TestHandleAverageSuccess(t *testing.T) {
	fake := &fakeAdvantage{data: stockData("IBM", map[string]advantage.TimeSerie{
		"2026-09-20": serie("1.5", "3.5", "0.5", "10"),
		"2026-09-21": serie("2.5", "4.5", "1.5", "20"),
		"2026-09-22": serie("3.5", "5.5", "2.5", "30"),
	})}
	srv := newTestServer(t, fake, 3)

	rec := doGet(t, srv, http.MethodGet)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	got := decodeTicker(t, rec)
	if got.Symbol != "IBM" {
		t.Errorf("symbol = %q, want IBM", got.Symbol)
	}
	if got.Average != 20 {
		t.Errorf("average = %v, want 20", got.Average)
	}
	if len(got.RawData) != 3 {
		t.Fatalf("len(data) = %d, want 3", len(got.RawData))
	}
	assertDescending(t, got.RawData)

	// Spot-check that every field survives the string -> float conversion.
	newest := got.RawData[0]
	if newest.Open != 3.5 || newest.High != 5.5 || newest.Low != 2.5 || newest.Close != 30 {
		t.Errorf("newest entry = %+v, want open 3.5 high 5.5 low 2.5 close 30", newest)
	}
}

func TestHandleAverageUpstreamError(t *testing.T) {
	fake := &fakeAdvantage{err: errors.New("alphavantage exploded")}
	srv := newTestServer(t, fake, 3)

	rec := doGet(t, srv, http.MethodGet)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "Internal Server Error" {
		t.Errorf("body = %q, want %q", body, "Internal Server Error")
	}
	// The upstream message must not reach the client.
	if strings.Contains(rec.Body.String(), "exploded") {
		t.Error("response body leaked the upstream error")
	}
}

func TestHandleAverageEmptySeries(t *testing.T) {
	fake := &fakeAdvantage{data: stockData("IBM", map[string]advantage.TimeSerie{})}
	srv := newTestServer(t, fake, 3)

	rec := doGet(t, srv, http.MethodGet)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	got := decodeTicker(t, rec)
	if got.Average != 0 {
		t.Errorf("average = %v, want 0", got.Average)
	}
	if math.IsNaN(float64(got.Average)) {
		t.Error("average is NaN")
	}
	if got.RawData == nil {
		t.Error("data = null, want []")
	}
	if len(got.RawData) != 0 {
		t.Errorf("len(data) = %d, want 0", len(got.RawData))
	}
}

func TestHandleAverageSkipsUnparseableRows(t *testing.T) {
	fake := &fakeAdvantage{data: stockData("IBM", map[string]advantage.TimeSerie{
		"2026-09-22": closeOnly("30"),
		"2026-09-21": closeOnly("abc"),           // close is not a number
		"not-a-date": closeOnly("40"),            // key is not a date
		"2026-09-20": serie("x", "1", "1", "10"), // open is not a number
		"2026-09-19": closeOnly("10"),
	})}
	srv := newTestServer(t, fake, 10)

	rec := doGet(t, srv, http.MethodGet)

	got := decodeTicker(t, rec)
	if len(got.RawData) != 2 {
		t.Fatalf("len(data) = %d, want 2 (only the two well-formed rows)", len(got.RawData))
	}
	if got.Average != 20 { // (30 + 10) / 2
		t.Errorf("average = %v, want 20", got.Average)
	}
}

func TestHandleAverageHonoursNDays(t *testing.T) {
	rows := map[string]advantage.TimeSerie{
		"2026-09-15": closeOnly("10"),
		"2026-09-16": closeOnly("20"),
		"2026-09-17": closeOnly("30"),
		"2026-09-18": closeOnly("40"),
		"2026-09-19": closeOnly("50"),
		"2026-09-20": closeOnly("60"),
		"2026-09-21": closeOnly("70"),
		"2026-09-22": closeOnly("80"),
	}
	want := []string{"2026-09-22", "2026-09-21", "2026-09-20"}

	for i := range shuffleRuns {
		srv := newTestServer(t, &fakeAdvantage{data: stockData("IBM", rows)}, 3)
		got := decodeTicker(t, doGet(t, srv, http.MethodGet))

		if len(got.RawData) != 3 {
			t.Fatalf("run %d: len(data) = %d, want 3", i, len(got.RawData))
		}
		for j, date := range want {
			if actual := got.RawData[j].Date.Format(time.DateOnly); actual != date {
				t.Fatalf("run %d: data[%d].date = %s, want %s (the N most recent days)", i, j, actual, date)
			}
		}
		if got.Average != 70 { // (80 + 70 + 60) / 3
			t.Fatalf("run %d: average = %v, want 70", i, got.Average)
		}
	}
}

func TestHandleAveragePassesRequestContext(t *testing.T) {
	fake := &fakeAdvantage{data: stockData("IBM", map[string]advantage.TimeSerie{
		"2026-09-22": closeOnly("30"),
	})}
	srv := newTestServer(t, fake, 3)

	type ctxKey struct{}
	req := httptest.NewRequest(http.MethodGet, "/average", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, "sentinel"))
	srv.Routes().ServeHTTP(httptest.NewRecorder(), req)

	if fake.calls != 1 {
		t.Fatalf("GetTickerData called %d times, want 1", fake.calls)
	}
	if fake.gotCtx == nil {
		t.Fatal("GetTickerData got a nil context")
	}
	if v := fake.gotCtx.Value(ctxKey{}); v != "sentinel" {
		t.Errorf("context value = %v, want the request's context to be propagated", v)
	}
}

func TestHandleAverageRejectsNonGET(t *testing.T) {
	fake := &fakeAdvantage{}
	srv := newTestServer(t, fake, 3)

	rec := doGet(t, srv, http.MethodPost)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if fake.calls != 0 {
		t.Errorf("GetTickerData called %d times on a rejected method, want 0", fake.calls)
	}
}

func TestNewTickerFromStockData(t *testing.T) {
	t.Run("sorts descending by date", func(t *testing.T) {
		data := stockData("IBM", map[string]advantage.TimeSerie{
			"2026-09-20": closeOnly("10"),
			"2026-09-22": closeOnly("30"),
			"2026-09-21": closeOnly("20"),
			"2026-09-19": closeOnly("5"),
		})
		for i := range shuffleRuns {
			got := NewTickerFromStockData(data, 0)
			if len(got.RawData) != 4 {
				t.Fatalf("run %d: len(data) = %d, want 4", i, len(got.RawData))
			}
			assertDescending(t, got.RawData)
		}
	})

	t.Run("copies the symbol", func(t *testing.T) {
		got := NewTickerFromStockData(stockData("AAPL", nil), 0)
		if got.Symbol != "AAPL" {
			t.Errorf("symbol = %q, want AAPL", got.Symbol)
		}
	})

	t.Run("nil series yields an empty, non-nil slice", func(t *testing.T) {
		got := NewTickerFromStockData(stockData("IBM", nil), 3)
		if got.RawData == nil {
			t.Fatal("RawData is nil; it would marshal as null instead of []")
		}
		if len(got.RawData) != 0 {
			t.Errorf("len(RawData) = %d, want 0", len(got.RawData))
		}
	})

	t.Run("days larger than the series keeps everything", func(t *testing.T) {
		got := NewTickerFromStockData(stockData("IBM", map[string]advantage.TimeSerie{
			"2026-09-22": closeOnly("30"),
			"2026-09-21": closeOnly("20"),
		}), 30)
		if len(got.RawData) != 2 {
			t.Errorf("len(RawData) = %d, want 2", len(got.RawData))
		}
	})
}

func TestCalculateAverageClose(t *testing.T) {
	tests := []struct {
		name   string
		closes []float32
		want   float32
	}{
		{name: "several entries", closes: []float32{10, 20, 30}, want: 20},
		{name: "single entry", closes: []float32{42}, want: 42},
		{name: "negative and positive", closes: []float32{-10, 10}, want: 0},
		{name: "empty", closes: nil, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ticker := Ticker{Symbol: "IBM"}
			for _, c := range tt.closes {
				ticker.RawData = append(ticker.RawData, Entry{Close: c})
			}

			ticker.CalculateAverageClose()

			if math.IsNaN(float64(ticker.Average)) {
				t.Fatal("average is NaN; it cannot be marshalled to JSON")
			}
			if ticker.Average != tt.want {
				t.Errorf("average = %v, want %v", ticker.Average, tt.want)
			}
		})
	}
}

func assertDescending(t *testing.T, entries []Entry) {
	t.Helper()
	for i := 1; i < len(entries); i++ {
		if !entries[i-1].Date.After(entries[i].Date) {
			t.Errorf("data not sorted descending: [%d] %s is not after [%d] %s",
				i-1, entries[i-1].Date.Format(time.DateOnly), i, entries[i].Date.Format(time.DateOnly))
		}
	}
}
