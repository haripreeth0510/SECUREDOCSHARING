package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"time"
)

type Config struct {
	App      AppConfig
	Database DatabaseConfig
	Redis    RedisConfig
	Auth     AuthConfig
	Storage  StorageConfig
	Keys     KeysConfig
}

// StorageConfig: set AccountURL (real Azure, keyless via Managed Identity or
// az login) or EmulatorEndpoint (local Azurite emulator only).
type StorageConfig struct {
	EmulatorEndpoint string
	AccountURL       string
	Container        string
}

// KeysConfig: set VaultURL + KEKName (Azure Key Vault) or LocalKEK (local
// development and tests only).
type KeysConfig struct {
	VaultURL string
	KEKName  string
	LocalKEK []byte
}

type AuthConfig struct {
	JWTSecret    []byte
	JWTIssuer    string
	JWTAudience  string
	AccessTTL    time.Duration
	RefreshTTL   time.Duration
	TermsVersion string
}

type AppConfig struct {
	Port string
	Env  string
}

// IsLocal reports whether the app runs without TLS (local development or CI),
// which is the only case where cookies may be sent without the Secure flag.
func (a AppConfig) IsLocal() bool { return a.Env == "development" || a.Env == "test" }

type DatabaseConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	Name     string
	SSLMode  string
}

type RedisConfig struct {
	Addr     string
	Password string
}

func (d DatabaseConfig) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		d.Host, d.Port, d.User, d.Password, d.Name, d.SSLMode,
	)
}

func Load() (*Config, error) {
	cfg := &Config{
		App: AppConfig{
			Port: getEnv("APP_PORT", "8080"),
			Env:  getEnv("APP_ENV", "development"),
		},
		Database: DatabaseConfig{
			Host:     getEnv("DB_HOST", "localhost"),
			Port:     getEnv("DB_PORT", "5432"),
			User:     getEnv("DB_USER", "postgres"),
			Password: getEnv("DB_PASSWORD", ""),
			Name:     getEnv("DB_NAME", "securedocsharing"),
			SSLMode:  getEnv("DB_SSLMODE", "disable"),
		},
		Redis: RedisConfig{
			Addr:     getEnv("REDIS_ADDR", "localhost:6379"),
			Password: getEnv("REDIS_PASSWORD", ""),
		},
	}

	if cfg.Database.Password == "" {
		return nil, fmt.Errorf("DB_PASSWORD is required")
	}

	cfg.Auth = AuthConfig{
		JWTSecret:    []byte(os.Getenv("JWT_SECRET")),
		JWTIssuer:    getEnv("JWT_ISSUER", "securedocsharing"),
		JWTAudience:  getEnv("JWT_AUDIENCE", "securedocsharing-api"),
		TermsVersion: getEnv("TERMS_VERSION", "1.0"),
	}
	if len(cfg.Auth.JWTSecret) < 32 {
		return nil, fmt.Errorf("JWT_SECRET is required and must be at least 32 bytes (generate one with: openssl rand -base64 48)")
	}
	var err error
	if cfg.Auth.AccessTTL, err = getDuration("ACCESS_TOKEN_TTL", 15*time.Minute); err != nil {
		return nil, err
	}
	if cfg.Auth.RefreshTTL, err = getDuration("REFRESH_TOKEN_TTL", 7*24*time.Hour); err != nil {
		return nil, err
	}

	if err := loadAzure(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

func loadAzure(cfg *Config) error {
	cfg.Storage = StorageConfig{
		EmulatorEndpoint: os.Getenv("AZURITE_BLOB_ENDPOINT"),
		Container:        getEnv("AZURE_STORAGE_CONTAINER", "documents"),
	}
	if acct := os.Getenv("AZURE_STORAGE_ACCOUNT"); acct != "" {
		cfg.Storage.AccountURL = "https://" + acct + ".blob.core.windows.net/"
	}
	switch {
	case cfg.Storage.AccountURL != "" && cfg.Storage.EmulatorEndpoint != "":
		return fmt.Errorf("set AZURE_STORAGE_ACCOUNT or AZURITE_BLOB_ENDPOINT, not both")
	case cfg.Storage.AccountURL == "" && cfg.Storage.EmulatorEndpoint == "":
		return fmt.Errorf("blob storage is not configured: set AZURE_STORAGE_ACCOUNT, or AZURITE_BLOB_ENDPOINT for the local emulator")
	case cfg.Storage.EmulatorEndpoint != "" && !cfg.App.IsLocal():
		// Deployed environments must use real Azure with Managed Identity.
		return fmt.Errorf("AZURITE_BLOB_ENDPOINT is only allowed when APP_ENV is development or test")
	}

	cfg.Keys = KeysConfig{
		VaultURL: os.Getenv("AZURE_KEYVAULT_URL"),
		KEKName:  getEnv("AZURE_KEK_NAME", "document-kek"),
	}
	local := os.Getenv("LOCAL_KEK")
	switch {
	case cfg.Keys.VaultURL != "" && local != "":
		return fmt.Errorf("set AZURE_KEYVAULT_URL or LOCAL_KEK, not both")
	case cfg.Keys.VaultURL != "":
		return nil
	case local == "":
		return fmt.Errorf("key management is not configured: set AZURE_KEYVAULT_URL, or LOCAL_KEK for local development")
	case !cfg.App.IsLocal():
		return fmt.Errorf("LOCAL_KEK is only allowed when APP_ENV is development or test; use Azure Key Vault")
	}
	kek, err := base64.StdEncoding.DecodeString(local)
	if err != nil || len(kek) != 32 {
		return fmt.Errorf("LOCAL_KEK must be 32 random bytes, base64 encoded (generate with: openssl rand -base64 32)")
	}
	cfg.Keys.LocalKEK = kek
	return nil
}

func getDuration(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration such as 15m or 168h", key)
	}
	return d, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
