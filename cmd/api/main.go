package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/securedocsharing/app/internal/auth"
	"github.com/securedocsharing/app/internal/config"
	"github.com/securedocsharing/app/internal/database"
	"github.com/securedocsharing/app/internal/handler"
	"github.com/securedocsharing/app/internal/keys"
	"github.com/securedocsharing/app/internal/ratelimit"
	"github.com/securedocsharing/app/internal/repository"
	"github.com/securedocsharing/app/internal/service"
	"github.com/securedocsharing/app/internal/storage"
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

	authSvc, limiter, err := buildAuth(ctx, cfg, pool, redisClient, logger)
	if err != nil {
		logger.Error("init auth", "error", err)
		os.Exit(1)
	}

	blobs, kek, err := buildAzure(ctx, cfg)
	if err != nil {
		logger.Error("init storage and keys", "error", err)
		os.Exit(1)
	}
	// Not used by any route yet; the upload/download work consumes them.
	// Building them here means a misconfiguration fails at startup and
	// /ready reports their health from day one.
	storageMode, keysMode := "azure", "key-vault"
	if cfg.Storage.EmulatorEndpoint != "" {
		storageMode = "azurite"
	}
	if cfg.Keys.VaultURL == "" {
		keysMode = "local"
	}
	logger.Info("blob storage and key management configured", "storage", storageMode, "keys", keysMode)

	mux := http.NewServeMux()

	healthHandler := handler.NewHealthHandler(
		handler.Check{Name: "database", Ping: pool.Ping},
		handler.Check{Name: "redis", Ping: func(ctx context.Context) error { return redisClient.Ping(ctx).Err() }},
		handler.Check{Name: "blob_storage", Ping: blobs.Ping},
		handler.Check{Name: "key_management", Ping: kek.Ping},
	)
	healthHandler.RegisterRoutes(mux)

	authHandler := handler.NewAuthHandler(authSvc, limiter, handler.AuthLimits{
		Register: handler.RouteLimit{Limit: 5, Window: 10 * time.Minute},
		Login:    handler.RouteLimit{Limit: 5, Window: time.Minute}, // plan: 5 attempts/min per IP
		Refresh:  handler.RouteLimit{Limit: 30, Window: time.Minute},
	}, !cfg.App.IsLocal(), logger)
	authHandler.RegisterRoutes(mux)

	srv := &http.Server{
		Addr:              ":" + cfg.App.Port,
		Handler:           handler.Recoverer(logger, handler.NoStore(mux)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
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

// buildAuth wires the auth service and the shared rate limiter.
func buildAuth(ctx context.Context, cfg *config.Config, pool *pgxpool.Pool, rdb *redis.Client, logger *slog.Logger) (*service.AuthService, ratelimit.Limiter, error) {
	issuer, err := auth.NewTokenIssuer(cfg.Auth.JWTSecret, cfg.Auth.JWTIssuer, cfg.Auth.JWTAudience, cfg.Auth.AccessTTL)
	if err != nil {
		return nil, nil, err
	}
	// Each hash holds 64 MiB, so at most 8 run at once (512 MiB ceiling).
	hasher := auth.NewPasswordHasher(auth.DefaultArgon2Params, 8)
	limiter := ratelimit.NewRedis(rdb, "sds:")

	svc, err := service.NewAuthService(ctx,
		repository.NewUserRepo(pool),
		repository.NewRefreshTokenRepo(pool),
		repository.NewAuditRepo(pool),
		hasher, issuer,
		auth.NewRedisDenylist(rdb, "sds:"),
		limiter,
		service.AuthConfig{
			RefreshTTL:   cfg.Auth.RefreshTTL,
			TermsVersion: cfg.Auth.TermsVersion,
			LoginLimit:   service.LoginLimit{Limit: 10, Window: 15 * time.Minute},
		},
		logger,
	)
	return svc, limiter, err
}

// buildAzure creates the blob store and key wrapper from config.
func buildAzure(ctx context.Context, cfg *config.Config) (storage.BlobStore, keys.KeyWrapper, error) {
	scfg := storage.Config{
		AccountURL: cfg.Storage.AccountURL,
		Container:  cfg.Storage.Container,
	}
	if cfg.Storage.EmulatorEndpoint != "" {
		scfg.ConnectionString = storage.AzuriteConnectionString(cfg.Storage.EmulatorEndpoint)
	}
	if scfg.ConnectionString != "" { // local Azurite: create the container on first run
		if err := storage.EnsureContainer(ctx, scfg); err != nil {
			return nil, nil, err
		}
	}
	blobs, err := storage.NewAzure(scfg)
	if err != nil {
		return nil, nil, err
	}

	var kek keys.KeyWrapper
	if cfg.Keys.VaultURL != "" {
		kek, err = keys.NewKeyVault(cfg.Keys.VaultURL, cfg.Keys.KEKName)
	} else {
		kek, err = keys.NewLocal(cfg.Keys.LocalKEK)
	}
	if err != nil {
		return nil, nil, err
	}
	return blobs, kek, nil
}
