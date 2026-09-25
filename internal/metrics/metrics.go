// Package metrics owns the OpenTelemetry instruments exported by the server.
package metrics

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/bradfitz/gomemcache/memcache"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

const (
	meterName   = "ticker"
	serviceName = "ticker-tracker"
)

var (
	// Memcache round-trips are sub-millisecond on a healthy LAN.
	cacheBuckets = []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1}
	// HTTP spans local handling through the 5s upstream client timeout.
	httpBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
)

// Fixed attribute sets, built once so the hot path does not allocate them.
var (
	outcomeOK    = metric.WithAttributeSet(attribute.NewSet(attribute.String("outcome", "ok")))
	outcomeError = metric.WithAttributeSet(attribute.NewSet(attribute.String("outcome", "error")))

	readHit   = metric.WithAttributeSet(attribute.NewSet(attribute.String("memcache.result", "hit")))
	readMiss  = metric.WithAttributeSet(attribute.NewSet(attribute.String("memcache.result", "miss")))
	readError = metric.WithAttributeSet(attribute.NewSet(attribute.String("memcache.result", "error")))

	writeOK    = metric.WithAttributeSet(attribute.NewSet(attribute.String("memcache.result", "ok")))
	writeError = metric.WithAttributeSet(attribute.NewSet(attribute.String("memcache.result", "error")))

	opGet = metric.WithAttributeSet(attribute.NewSet(attribute.String("memcache.operation", "get")))
	opSet = metric.WithAttributeSet(attribute.NewSet(attribute.String("memcache.operation", "set")))
)

type Metrics struct {
	requestDuration    metric.Float64Histogram // inbound
	upstreamDuration   metric.Float64Histogram // outbound AlphaVantage
	upstreamErrors     metric.Int64Counter
	cacheReadDuration  metric.Float64Histogram
	cacheWriteDuration metric.Float64Histogram
	cacheHits          metric.Int64Counter
	cacheErrors        metric.Int64Counter
}

func New(mp metric.MeterProvider) (*Metrics, error) {
	meter := mp.Meter(meterName)
	m := &Metrics{}

	histograms := []struct {
		dst     *metric.Float64Histogram
		name    string
		desc    string
		buckets []float64
	}{
		{&m.requestDuration, "http.server.request.duration", "Duration of inbound HTTP requests.", httpBuckets},
		{&m.upstreamDuration, "alphavantage.request.duration", "Duration of outbound AlphaVantage requests.", httpBuckets},
		{&m.cacheReadDuration, "memcache.read.duration", "Duration of memcache get operations.", cacheBuckets},
		{&m.cacheWriteDuration, "memcache.write.duration", "Duration of memcache set operations.", cacheBuckets},
	}
	for _, h := range histograms {
		inst, err := meter.Float64Histogram(h.name,
			metric.WithDescription(h.desc),
			metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(h.buckets...),
		)
		if err != nil {
			return nil, fmt.Errorf("creating instrument %s: %w", h.name, err)
		}
		*h.dst = inst
	}

	counters := []struct {
		dst  *metric.Int64Counter
		name string
		desc string
		unit string
	}{
		{&m.upstreamErrors, "alphavantage.request.errors", "Failed outbound AlphaVantage requests.", "{error}"},
		{&m.cacheHits, "memcache.hits", "Memcache reads that returned an item.", "{hit}"},
		{&m.cacheErrors, "memcache.errors", "Memcache operations that failed.", "{error}"},
	}
	for _, c := range counters {
		inst, err := meter.Int64Counter(c.name,
			metric.WithDescription(c.desc),
			metric.WithUnit(c.unit),
		)
		if err != nil {
			return nil, fmt.Errorf("creating instrument %s: %w", c.name, err)
		}
		*c.dst = inst
	}

	return m, nil
}

// NewPrometheusProvider returns a meter provider whose metrics are exposed on
// the default Prometheus registry, served by Handler.
func NewPrometheusProvider() (*sdkmetric.MeterProvider, error) {
	exp, err := prometheus.New()
	if err != nil {
		return nil, fmt.Errorf("creating prometheus exporter: %w", err)
	}
	return sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(exp),
		sdkmetric.WithResource(resource.NewSchemaless(attribute.String("service.name", serviceName))),
	), nil
}

// Handler serves the default Prometheus registry in the text exposition format.
func Handler() http.Handler {
	return promhttp.Handler()
}

// normalizeMethod bounds label cardinality: arbitrary client methods collapse to _OTHER.
func normalizeMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete,
		http.MethodConnect, http.MethodOptions, http.MethodTrace, http.MethodPatch:
		return method
	}
	return "_OTHER"
}

func (m *Metrics) RecordRequest(ctx context.Context, method, route string, status int, d time.Duration) {
	m.requestDuration.Record(ctx, d.Seconds(), metric.WithAttributes(
		attribute.String("http.request.method", normalizeMethod(method)),
		attribute.String("http.route", route),
		attribute.Int("http.response.status_code", status),
	))
}

func (m *Metrics) RecordUpstream(ctx context.Context, d time.Duration, err error) {
	if err == nil {
		m.upstreamDuration.Record(ctx, d.Seconds(), outcomeOK)
		return
	}
	m.upstreamDuration.Record(ctx, d.Seconds(), outcomeError)
	m.upstreamErrors.Add(ctx, 1)
}

// RecordCacheRead counts a hit whenever memcache returned an item, regardless
// of whether the caller can decode it. A miss is not an error.
func (m *Metrics) RecordCacheRead(ctx context.Context, d time.Duration, err error) {
	switch {
	case err == nil:
		m.cacheReadDuration.Record(ctx, d.Seconds(), readHit)
		m.cacheHits.Add(ctx, 1)
	case errors.Is(err, memcache.ErrCacheMiss):
		m.cacheReadDuration.Record(ctx, d.Seconds(), readMiss)
	default:
		m.cacheReadDuration.Record(ctx, d.Seconds(), readError)
		m.cacheErrors.Add(ctx, 1, opGet)
	}
}

func (m *Metrics) RecordCacheWrite(ctx context.Context, d time.Duration, err error) {
	if err == nil {
		m.cacheWriteDuration.Record(ctx, d.Seconds(), writeOK)
		return
	}
	m.cacheWriteDuration.Record(ctx, d.Seconds(), writeError)
	m.cacheErrors.Add(ctx, 1, opSet)
}
