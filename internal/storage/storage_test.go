package storage_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/securedocsharing/app/internal/storage"
)

// newStore runs against the Azurite emulator at AZURITE_BLOB_ENDPOINT.
func newStore(t *testing.T) storage.BlobStore {
	t.Helper()
	ep := os.Getenv("AZURITE_BLOB_ENDPOINT")
	if ep == "" {
		t.Skip("AZURITE_BLOB_ENDPOINT not set (start Azurite: docker compose up -d azurite)")
	}
	cs := storage.AzuriteConnectionString(ep)
	cfg := storage.Config{ConnectionString: cs, Container: "test-" + randHex(4)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := storage.EnsureContainer(ctx, cfg); err != nil {
		t.Fatalf("blob emulator configured but not usable: %v", err)
	}
	s, err := storage.NewAzure(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func TestPutGetDelete(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	// 9 MiB spans several 4 MiB blocks.
	data := make([]byte, 9<<20)
	_, _ = rand.Read(data)
	name := "docs/" + randHex(8) + ".enc"

	if err := s.Put(ctx, name, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	rc, err := s.Get(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("round trip mismatch (len %d vs %d, err %v)", len(got), len(data), err)
	}

	// Never overwrite an existing blob.
	if err := s.Put(ctx, name, bytes.NewReader([]byte("other"))); !errors.Is(err, storage.ErrAlreadyExists) {
		t.Fatalf("overwrite: want ErrAlreadyExists, got %v", err)
	}

	if err := s.Delete(ctx, name); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, name); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("after delete: want ErrNotFound, got %v", err)
	}
	if err := s.Delete(ctx, name); err != nil {
		t.Fatalf("deleting a missing blob must succeed, got %v", err)
	}
}

func TestPingMissingContainer(t *testing.T) {
	ep := os.Getenv("AZURITE_BLOB_ENDPOINT")
	if ep == "" {
		t.Skip("AZURITE_BLOB_ENDPOINT not set")
	}
	cs := storage.AzuriteConnectionString(ep)
	s, err := storage.NewAzure(storage.Config{ConnectionString: cs, Container: "does-not-exist-" + randHex(4)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Ping(ctx); err == nil {
		t.Fatal("Ping must fail for a missing container")
	}
}

func TestConfigValidation(t *testing.T) {
	for name, cfg := range map[string]storage.Config{
		"no container": {ConnectionString: "x"},
		"no endpoint":  {Container: "c"},
		"both":         {ConnectionString: "x", AccountURL: "https://a.blob.core.windows.net/", Container: "c"},
	} {
		if _, err := storage.NewAzure(cfg); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// TestAzureLive runs only against a real storage account (AZURE_STORAGE_ACCOUNT
// set, az login / Managed Identity available): keyless auth, put, get, delete.
func TestAzureLive(t *testing.T) {
	acct := os.Getenv("AZURE_STORAGE_ACCOUNT")
	if acct == "" {
		t.Skip("AZURE_STORAGE_ACCOUNT not set")
	}
	container := os.Getenv("AZURE_STORAGE_CONTAINER")
	if container == "" {
		container = "documents"
	}
	s, err := storage.NewAzure(storage.Config{AccountURL: "https://" + acct + ".blob.core.windows.net/", Container: container})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	name := "live-test/" + randHex(8) + ".bin"
	data := []byte("live test " + randHex(16))
	if err := s.Put(ctx, name, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Delete(context.Background(), name) })
	rc, err := s.Get(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(got, data) {
		t.Fatal("round trip mismatch")
	}
	if err := s.Delete(ctx, name); err != nil {
		t.Fatal(err)
	}
}
