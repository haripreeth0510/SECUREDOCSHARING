package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/securedocsharing/app/internal/config"
	"github.com/securedocsharing/app/internal/database"
	"github.com/securedocsharing/app/internal/handler"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		logger.Error("load config", "error", err)
		os.Exit(1)
	}

	if err := database.RunMigrations(cfg.Database); err != nil {
		logger.Error("run migrations", "error", err)
		os.Exit(1)
	}
	logger.Info("migrations applied")

	ctx := context.Background()

	pool, err := database.NewPool(ctx, cfg.Database)
	if err != nil {
		logger.Error("connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	logger.Info("database connected")

	redisClient, err := database.NewRedis(ctx, cfg.Redis)
	if err != nil {
		logger.Error("connect to redis", "error", err)
		os.Exit(1)
	}
	defer redisClient.Close()
	logger.Info("redis connected")

	mux := http.NewServeMux()

	healthHandler := handler.NewHealthHandler(pool, redisClient)
	healthHandler.RegisterRoutes(mux)

	srv := &http.Server{
		Addr:         ":" + cfg.App.Port,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		logger.Info("server starting", "port", cfg.App.Port, "env", cfg.App.Env)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutting down gracefully")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", "error", err)
	}
}
