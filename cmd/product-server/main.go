package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/product"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	config, err := product.LoadConfig()
	if err != nil {
		logger.Error("invalid product configuration", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	repository, err := product.OpenPostgres(ctx, config.DatabaseURL)
	if err != nil {
		logger.Error("open postgres", "error", err)
		os.Exit(1)
	}
	defer repository.Close()
	if err := repository.Migrate(ctx); err != nil {
		logger.Error("migrate postgres", "error", err)
		os.Exit(1)
	}

	redisOptions, err := redis.ParseURL(config.RedisURL)
	if err != nil {
		logger.Error("parse redis URL", "error", err)
		os.Exit(1)
	}
	redisClient := redis.NewClient(redisOptions)
	defer func() { _ = redisClient.Close() }()
	coordinator := product.NewRedisCoordinator(redisClient, "northstar:product")

	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	metrics := product.NewMetrics(registry)
	service := product.NewService(repository, repository, coordinator, metrics, logger, config.OutboundWebhookURL, config.WebhookMaxAttempts)
	worker := product.NewWebhookWorker(repository, &http.Client{Timeout: config.WebhookRequestTimeout}, config.OutboundWebhookSecret, config.WebhookLease, config.WebhookPollInterval, metrics, logger)
	go worker.Run(ctx)

	server := &http.Server{
		Addr:              config.Address,
		Handler:           product.NewHTTPServer(service, config, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}), logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		logger.Info("product API listening", "address", config.Address)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("product API stopped unexpectedly", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
	}
}
