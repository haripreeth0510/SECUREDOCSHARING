package keys_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/securedocsharing/app/internal/keys"
)

func kek(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func TestLocalWrapUnwrap(t *testing.T) {
	ctx := context.Background()
	w, err := keys.NewLocal(kek(1))
	if err != nil {
		t.Fatal(err)
	}
	dek, err := keys.NewDEK()
	if err != nil || len(dek) != keys.DEKSize {
		t.Fatalf("NewDEK: %v", err)
	}

	wrapped, id, err := w.Wrap(ctx, dek)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(wrapped, dek) {
		t.Fatal("wrapped key contains the plaintext DEK")
	}
	got, err := w.Unwrap(ctx, wrapped, id)
	if err != nil || !bytes.Equal(got, dek) {
		t.Fatalf("unwrap: %v", err)
	}

	// Wrapping twice gives different ciphertext (random nonce).
	again, _, _ := w.Wrap(ctx, dek)
	if bytes.Equal(again, wrapped) {
		t.Fatal("wrap must be randomised")
	}
}

func TestLocalRejectsTamperingAndWrongKey(t *testing.T) {
	ctx := context.Background()
	w, _ := keys.NewLocal(kek(1))
	other, _ := keys.NewLocal(kek(2))
	dek, _ := keys.NewDEK()
	wrapped, id, _ := w.Wrap(ctx, dek)

	tampered := append([]byte(nil), wrapped...)
	tampered[len(tampered)-1] ^= 1
	if _, err := w.Unwrap(ctx, tampered, id); err == nil {
		t.Fatal("tampered wrapped key accepted")
	}
	// A different KEK has a different ID and is refused before decrypting.
	if _, err := other.Unwrap(ctx, wrapped, id); !errors.Is(err, keys.ErrUnknownKEK) {
		t.Fatalf("wrong KEK: want ErrUnknownKEK, got %v", err)
	}
	if _, err := w.Unwrap(ctx, wrapped, "https://vault/keys/document-kek/abc"); !errors.Is(err, keys.ErrUnknownKEK) {
		t.Fatalf("foreign KEK id: want ErrUnknownKEK, got %v", err)
	}
	if _, err := w.Unwrap(ctx, []byte{1, 2}, id); err == nil {
		t.Fatal("truncated input accepted")
	}
}

func TestLocalInputValidation(t *testing.T) {
	if _, err := keys.NewLocal([]byte("short")); err == nil {
		t.Fatal("short KEK accepted")
	}
	w, _ := keys.NewLocal(kek(1))
	if _, _, err := w.Wrap(context.Background(), []byte("16-byte-key-1234")); !errors.Is(err, keys.ErrInvalidDEK) {
		t.Fatalf("want ErrInvalidDEK, got %v", err)
	}
}

func TestKeyVaultConfigValidation(t *testing.T) {
	if _, err := keys.NewKeyVault("", "k"); err == nil {
		t.Fatal("missing vault URL accepted")
	}
}
