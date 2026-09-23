package advantage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"
)

type ErrorMsg struct {
	Msg string `json:"Error Message"`
}

type StockData struct {
	Metadata Metadata             `json:"Meta Data"`
	Series   map[string]TimeSerie `json:"Time Series (Daily)"`
}

type Metadata struct {
	Symbol string `json:"2. Symbol"`
}

type TimeSerie struct {
	Open  string `json:"1. open"`
	High  string `json:"2. high"`
	Low   string `json:"3. low"`
	Close string `json:"4. close"`
}

const AdvantageUrl = "https://www.alphavantage.co/query"

type IAdvantage interface {
	GetTickerData(ctx context.Context) (StockData, error)
}

type AdvantageClient struct {
	key    string
	symbol string
	nDays  int
	http   *http.Client
}

func (c *AdvantageClient) GetTickerData(ctx context.Context) (StockData, error) {
	url := fmt.Sprintf("%s?apikey=%s&function=TIME_SERIES_DAILY&symbol=%s", AdvantageUrl, c.key, c.symbol)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return StockData{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return StockData{}, err
	}
	logrus.Infof("AlphaAdvantage resp Status Code: %d", resp.StatusCode)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return StockData{}, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, body)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return StockData{}, fmt.Errorf("unexpected error reading response body: %w", err)
	}

	// Missing api response is a 200 with an error message, so we decode that and check for it.
	var errMsg ErrorMsg
	json.Unmarshal(body, &errMsg)
	if errMsg.Msg != "" {
		return StockData{}, fmt.Errorf("unable to complete request: %s", errMsg.Msg)
	}

	var data StockData
	json.Unmarshal(body, &data)

	return data, nil
}

func NewAdvantageClient(key string, symbol string, nDays int) AdvantageClient {
	client := http.Client{Timeout: 5 * time.Second}
	return AdvantageClient{key, symbol,
		nDays, &client}
}
