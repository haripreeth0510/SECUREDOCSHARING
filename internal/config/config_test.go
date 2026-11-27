package config

import (
	"strings"
	"testing"
)

const azurite = "http://127.0.0.1:10000/devstoreaccount1"

// base sets a valid local configuration; each test then breaks one thing.
func base(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{
		"APP_ENV":               "development",
		"DB_PASSWORD":           "pw",
		"JWT_SECRET":            strings.Repeat("s", 32),
		"AZURITE_BLOB_ENDPOINT": azurite,
		"AZURE_STORAGE_ACCOUNT": "",
		"AZURE_KEYVAULT_URL":    "",
		"LOCAL_KEK":             "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=",
		"ACCESS_TOKEN_TTL":      "",
		"REFRESH_TOKEN_TTL":     "",
	} {
		t.Setenv(k, v)
	}
}

func TestLoadValidLocal(t *testing.T) {
	base(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.AccessTTL.Minutes() != 15 || cfg.Auth.RefreshTTL.Hours() != 168 || len(cfg.Keys.LocalKEK) != 32 {
		t.Fatalf("unexpected defaults: %+v %+v", cfg.Auth, cfg.Keys)
	}
}

func TestLoadValidProduction(t *testing.T) {
	base(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("AZURITE_BLOB_ENDPOINT", "")
	t.Setenv("AZURE_STORAGE_ACCOUNT", "sdsprod")
	t.Setenv("LOCAL_KEK", "")
	t.Setenv("AZURE_KEYVAULT_URL", "https://kv-sds-prod.vault.azure.net/")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.AccountURL != "https://sdsprod.blob.core.windows.net/" || cfg.App.IsLocal() {
		t.Fatalf("unexpected config: %+v", cfg.Storage)
	}
}

func TestLoadRejects(t *testing.T) {
	cases := map[string]map[string]string{
		"missing JWT secret":        {"JWT_SECRET": ""},
		"short JWT secret":          {"JWT_SECRET": "too-short"},
		"bad TTL":                   {"ACCESS_TOKEN_TTL": "-5m"},
		"no storage":                {"AZURITE_BLOB_ENDPOINT": ""},
		"both storage modes":        {"AZURE_STORAGE_ACCOUNT": "x"},
		"no keys":                   {"LOCAL_KEK": ""},
		"both key modes":            {"AZURE_KEYVAULT_URL": "https://kv.vault.azure.net/"},
		"bad local KEK":             {"LOCAL_KEK": "c2hvcnQ="},
		"emulator in prod":          {"APP_ENV": "production", "LOCAL_KEK": "", "AZURE_KEYVAULT_URL": "https://kv.vault.azure.net/"},
		"local KEK in prod":         {"APP_ENV": "production", "AZURITE_BLOB_ENDPOINT": "", "AZURE_STORAGE_ACCOUNT": "x"},
		"local KEK in staging":      {"APP_ENV": "staging", "AZURITE_BLOB_ENDPOINT": "", "AZURE_STORAGE_ACCOUNT": "x"},
		"missing database password": {"DB_PASSWORD": ""},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			base(t)
			for k, v := range env {
				t.Setenv(k, v)
			}
			if _, err := Load(); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
