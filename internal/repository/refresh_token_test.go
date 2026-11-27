package repository_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/securedocsharing/app/internal/repository"
)

func TestCreateWithConsent(t *testing.T) {
	ctx := context.Background()
	tx := newTx(t)
	repo := repository.NewUserRepo(tx)

	u, err := repo.CreateWithConsent(ctx, "consent@example.com", "hash", "1.0", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var version string
	if err := tx.QueryRow(ctx,
		`SELECT version FROM consent_log WHERE user_id = $1::uuid AND document_name = 'terms'`, u.ID,
	).Scan(&version); err != nil || version != "1.0" {
		t.Fatalf("consent row: %q %v", version, err)
	}

	sp, _ := tx.Begin(ctx)
	if _, err := repository.NewUserRepo(sp).CreateWithConsent(ctx, "CONSENT@example.com", "h", "1.0", time.Now()); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("duplicate: want ErrConflict, got %v", err)
	}
	_ = sp.Rollback(ctx)

	if err := repo.UpdatePasswordHash(ctx, u.ID, "new-hash"); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.FindByID(ctx, u.ID); got.PasswordHash != "new-hash" {
		t.Fatalf("password hash not updated: %q", got.PasswordHash)
	}
	if err := repo.UpdatePasswordHash(ctx, "bad-id", "x"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("bad id: want ErrNotFound, got %v", err)
	}
}

func TestRefreshTokenRotation(t *testing.T) {
	ctx := context.Background()
	tx := newTx(t)
	repo := repository.NewRefreshTokenRepo(tx)
	u := mustUser(t, tx, "rt@example.com")
	exp := time.Now().Add(time.Hour)

	if err := repo.Create(ctx, u.ID, "h1", exp); err != nil {
		t.Fatal(err)
	}

	// h1 -> h2
	userID, err := repo.Rotate(ctx, "h1", "h2", exp)
	if err != nil || userID != u.ID {
		t.Fatalf("rotate: %q %v", userID, err)
	}

	// Presenting h1 again is reuse and reports the owner.
	userID, err = repo.Rotate(ctx, "h1", "h3", exp)
	if !errors.Is(err, repository.ErrTokenReused) || userID != u.ID {
		t.Fatalf("reuse: want ErrTokenReused for %s, got %q %v", u.ID, userID, err)
	}
	// The failed rotation must not have stored h3.
	if _, err := repo.Rotate(ctx, "h3", "h4", exp); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("h3 must not exist, got %v", err)
	}

	// Unknown token.
	if _, err := repo.Rotate(ctx, "nope", "h5", exp); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("unknown: want ErrNotFound, got %v", err)
	}

	// RevokeAllForUser kills the live h2.
	if err := repo.RevokeAllForUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Rotate(ctx, "h2", "h6", exp); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("after revoke all: want ErrNotFound, got %v", err)
	}
}

func TestRefreshTokenExpiredAndRevoke(t *testing.T) {
	ctx := context.Background()
	tx := newTx(t)
	repo := repository.NewRefreshTokenRepo(tx)
	u := mustUser(t, tx, "rt2@example.com")

	if err := repo.Create(ctx, u.ID, "expired", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Rotate(ctx, "expired", "x", time.Now().Add(time.Hour)); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("expired: want ErrNotFound, got %v", err)
	}

	if err := repo.Create(ctx, u.ID, "live", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if id, err := repo.Revoke(ctx, "live"); err != nil || id != u.ID {
		t.Fatalf("revoke: %q %v", id, err)
	}
	if _, err := repo.Revoke(ctx, "live"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("second revoke: want ErrNotFound, got %v", err)
	}
	// A token revoked by logout is not "reuse": it just stops working.
	if _, err := repo.Rotate(ctx, "live", "y", time.Now().Add(time.Hour)); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("logged-out token: want ErrNotFound, got %v", err)
	}
}
