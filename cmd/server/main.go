package main

import (
	"context"
	"errors"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/bradfitz/gomemcache/memcache"
	"github.com/sirupsen/logrus"

	"ticker/internal/advantage"
	"ticker/internal/api"
	"ticker/internal/config"
	"ticker/internal/metrics"
)

const defaultAddr = ":8080"

const shutdownTimeout = 10 * time.Second

func main() {
	log := logrus.New()
	log.SetFormatter(&logrus.TextFormatter{})

	cfg, err := config.Load()
	if err != nil {
		log.WithError(err).Fatal("configuration error")
	}

	client := advantage.NewAdvantageClient(cfg.APIKey, cfg.Ticker)
	store := memcache.New(cfg.MemcachedAddr)
	if err := store.Ping(); err != nil {
		log.WithError(err).WithField("addr", cfg.MemcachedAddr).Fatal("memcached unreachable")
	}

	mp, err := metrics.NewPrometheusProvider()
	if err != nil {
		log.WithError(err).Fatal("metrics provider setup failed")
	}
	m, err := metrics.New(mp)
	if err != nil {
		log.WithError(err).Fatal("metrics instruments setup failed")
	}

	srv := &http.Server{
		Addr:              defaultAddr,
		Handler:           api.NewServer(cfg, log, &client, store, m).Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Metrics live on their own listener so the public ingress never exposes them.
	mmux := http.NewServeMux()
	mmux.Handle("GET /metrics", metrics.Handler())
	metricsSrv := &http.Server{
		Addr:              cfg.MetricsAddr,
		Handler:           mmux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 2)
	go func() {
		log.WithFields(logrus.Fields{"addr": defaultAddr, "ticker": cfg.Ticker}).Info("server listening")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	go func() {
		log.WithField("addr", cfg.MetricsAddr).Info("metrics server listening")
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		log.WithError(err).Fatal("server failed")
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.WithError(err).Fatal("graceful shutdown failed")
	}
	if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
		log.WithError(err).Error("metrics server shutdown failed")
	}
	if err := mp.Shutdown(shutdownCtx); err != nil {
		log.WithError(err).Error("meter provider shutdown failed")
	}
	log.Info("server stopped")
}
