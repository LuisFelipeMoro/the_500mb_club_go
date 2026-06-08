package main

import (
	"context"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/batch"
	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/handler"
	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/metrics"
	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/middleware"
	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/storage"
	"github.com/gofiber/fiber/v2"
	_ "go.uber.org/automaxprocs" // sets GOMAXPROCS from the cgroup CPU quota
	"go.uber.org/zap"
)

func main() {
	log, _ := zap.NewProduction()
	defer func() { _ = log.Sync() }()

	addr := env("REDIS_ADDR", "localhost:6379")
	instanceID := env("INSTANCE_ID", "api")
	listen := ":" + env("PORT", "3000")

	log.Info("starting pi-bench api",
		zap.String("instance", instanceID),
		zap.Int("gomaxprocs", runtime.GOMAXPROCS(0)),
	)

	store, err := storage.NewRueidisStore(addr)
	if err != nil {
		log.Fatal("redis connect failed", zap.Error(err))
	}

	m := metrics.New()
	writer := batch.New(store, 10000, log)
	go writer.Run()

	h := handler.New(store, writer, m, log)

	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
		ServerHeader:          "",
		ReadTimeout:           5 * time.Second,
		WriteTimeout:          5 * time.Second,
	})

	app.Use(middleware.Instrument(instanceID, m))

	app.Post("/devices/:id/telemetry", h.PostTelemetry)
	app.Post("/devices/:id/telemetry/batch", h.PostBatch)
	app.Get("/devices/:id/telemetry", h.GetTelemetry)
	app.Get("/devices/:id/anomaly", h.GetAnomaly)
	app.Get("/readyz", h.Readyz)
	app.Get("/healthz", h.Healthz)
	app.Get("/metrics", h.MetricsHandler())

	// Listen in the background so the main goroutine can wait for SIGTERM.
	go func() {
		if err := app.Listen(listen); err != nil {
			log.Fatal("listen failed", zap.Error(err))
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	<-ctx.Done()
	log.Info("shutdown signal received, draining")

	// 1. Stop accepting; wait for in-flight handlers to finish (≤10s).
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.ShutdownWithContext(shutdownCtx); err != nil {
		log.Error("graceful shutdown timed out", zap.Error(err))
	}
	// 2. Close the write channel → batch writer drains the remainder to Redis.
	writer.Close()
	// 3. Close the Redis connection.
	store.Close()
	log.Info("shutdown complete")
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
