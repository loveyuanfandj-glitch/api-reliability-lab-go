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

	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/app"
	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/httpapi"
	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/store"
	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/telemetry"
	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/upstream"
	"github.com/prometheus/client_golang/prometheus"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	metrics := telemetry.NewMetrics(prometheus.DefaultRegisterer)
	service := app.NewService(store.NewMemory(500), upstream.NewSimulator(), metrics)
	handler := httpapi.NewServer(service, metrics, logger).Handler()
	address := os.Getenv("ADDR")
	if address == "" {
		address = ":8080"
	}

	server := &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("northstar reliability lab listening", "address", address)
		errCh <- server.ListenAndServe()
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	select {
	case signal := <-signals:
		logger.Info("shutdown requested", "signal", signal.String())
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped unexpectedly", "error", err)
			os.Exit(1)
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}
	logger.Info("shutdown complete")
}
