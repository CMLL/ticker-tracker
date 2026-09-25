package config

import (
	"cmp"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	defaultMemcachedAddr = "localhost:11211"
	defaultMetricsAddr   = ":9090"
)

type Config struct {
	Ticker        string
	APIKey        string
	NDays         int
	MemcachedAddr string
	MetricsAddr   string
}

func Load() (*Config, error) {
	days := os.Getenv("NDAYS")
	ndays, err := strconv.Atoi(days)
	if err != nil {
		return nil, fmt.Errorf("unable to parse NDAYS: %s", days)
	}
	cfg := &Config{
		Ticker:        os.Getenv("TICKER"),
		APIKey:        os.Getenv("API_KEY"),
		NDays:         ndays,
		MemcachedAddr: cmp.Or(os.Getenv("MEMCACHED_ADDR"), defaultMemcachedAddr),
		MetricsAddr:   cmp.Or(os.Getenv("METRICS_ADDR"), defaultMetricsAddr),
	}

	var missing []string
	if cfg.Ticker == "" {
		missing = append(missing, "TICKER")
	}
	if cfg.APIKey == "" {
		missing = append(missing, "API_KEY")
	}
	if cfg.NDays == 0 {
		missing = append(missing, "NDAYS")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}

	return cfg, nil
}
