package repository_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/securedocsharing/app/internal/model"
	"github.com/securedocsharing/app/internal/repository"
)

// newTx opens a transaction on the test database and rolls it back when the
// test ends, so no rows are left behind. It skips when no database is configured.
func newTx(t *testing.T) pgx.Tx {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / DATABASE_URL not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("database not reachable: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	return tx
}

func mustUser(t *testing.T, db repository.DBTX, email string) *model.User {
	t.Helper()
	u, err := repository.NewUserRepo(db).Create(context.Background(), email, "hash", time.Now())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

func docParams(ownerID string) repository.CreateDocumentParams {
	return repository.CreateDocumentParams{
		OwnerID:     ownerID,
		Name:        "contract.pdf",
		ContentType: "application/pdf",
		SizeBytes:   1234,
		StorageURL:  "test/contract.enc",
		WrappedDEK:  []byte{1, 2, 3},
		KEKID:       "kek/v1",
		Nonce:       []byte{4, 5, 6},
	}
}

func mustDoc(t *testing.T, db repository.DBTX, ownerID string) *model.Document {
	t.Helper()
	d, err := repository.NewDocumentRepo(db).Create(context.Background(), docParams(ownerID))
	if err != nil {
		t.Fatalf("create document: %v", err)
	}
	return d
}

func TestUserRepo(t *testing.T) {
	ctx := context.Background()
	tx := newTx(t)
	repo := repository.NewUserRepo(tx)

	u := mustUser(t, tx, "alice@example.com")
	if u.ID == "" || u.Email != "alice@example.com" {
		t.Fatalf("unexpected user: %+v", u)
	}

	if got, err := repo.FindByID(ctx, u.ID); err != nil || got.ID != u.ID {
		t.Fatalf("FindByID: %v %+v", err, got)
	}
	// citext: lookup ignores case
	if got, err := repo.FindByEmail(ctx, "ALICE@EXAMPLE.COM"); err != nil || got.ID != u.ID {
		t.Fatalf("FindByEmail (case-insensitive): %v %+v", err, got)
	}

	// duplicate email (different case) must be a conflict; run it in a savepoint
	sp, err := tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.NewUserRepo(sp).Create(ctx, "Alice@Example.com", "hash", time.Now())
	if !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	_ = sp.Rollback(ctx)

	if _, err := repo.FindByID(ctx, "not-a-uuid"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("malformed id: want ErrNotFound, got %v", err)
	}

	if err := repo.Delete(ctx, u.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.FindByID(ctx, u.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("after delete: want ErrNotFound, got %v", err)
	}
	if err := repo.Delete(ctx, u.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("second delete: want ErrNotFound, got %v", err)
	}
}

func TestDocumentRepo(t *testing.T) {
	ctx := context.Background()
	tx := newTx(t)
	repo := repository.NewDocumentRepo(tx)

	owner := mustUser(t, tx, "owner@example.com")
	other := mustUser(t, tx, "other@example.com")
	d := mustDoc(t, tx, owner.ID)

	full, err := repo.FindByID(ctx, d.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if string(full.WrappedDEK) != string([]byte{1, 2, 3}) || full.KEKID != "kek/v1" {
		t.Fatalf("encryption metadata not stored: %+v", full)
	}

	if _, err := repo.FindByIDForOwner(ctx, d.ID, other.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("other user must not see the document, got %v", err)
	}
	if _, err := repo.FindByIDForOwner(ctx, d.ID, owner.ID); err != nil {
		t.Fatalf("owner lookup: %v", err)
	}

	// size above the 100 MB limit violates the CHECK constraint
	sp, err := tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	big := docParams(owner.ID)
	big.SizeBytes = 200 * 1024 * 1024
	if _, err := repository.NewDocumentRepo(sp).Create(ctx, big); err == nil {
		t.Fatal("expected an error for an oversized document")
	}
	_ = sp.Rollback(ctx)

	list, err := repo.ListByOwner(ctx, owner.ID, 10, 0)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListByOwner: %v len=%d", err, len(list))
	}

	if err := repo.SoftDelete(ctx, d.ID, other.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("non-owner delete: want ErrNotFound, got %v", err)
	}
	if err := repo.SoftDelete(ctx, d.ID, owner.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if _, err := repo.FindByID(ctx, d.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("soft-deleted document must be hidden, got %v", err)
	}
	if list, _ := repo.ListByOwner(ctx, owner.ID, 10, 0); len(list) != 0 {
		t.Fatalf("soft-deleted document still listed")
	}
}

func TestShareRepo(t *testing.T) {
	ctx := context.Background()
	tx := newTx(t)
	repo := repository.NewShareRepo(tx)

	owner := mustUser(t, tx, "owner@example.com")
	other := mustUser(t, tx, "other@example.com")
	d := mustDoc(t, tx, owner.ID)

	s, err := repo.Create(ctx, repository.CreateShareParams{
		DocumentID: d.ID, RecipientEmail: "bob@example.com", TokenHash: "hash-1",
		ExpiresAt: time.Now().Add(time.Hour), MaxOpens: 2,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got, err := repo.FindByTokenHash(ctx, "hash-1"); err != nil || got.ID != s.ID {
		t.Fatalf("FindByTokenHash: %v", err)
	}

	// duplicate token hash is a conflict
	sp, err := tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.NewShareRepo(sp).Create(ctx, repository.CreateShareParams{
		DocumentID: d.ID, RecipientEmail: "eve@example.com", TokenHash: "hash-1",
		ExpiresAt: time.Now().Add(time.Hour), MaxOpens: 1,
	})
	if !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	_ = sp.Rollback(ctx)

	// max opens is enforced
	for i := 1; i <= 2; i++ {
		got, err := repo.TryOpen(ctx, s.ID)
		if err != nil || got.OpenCount != i {
			t.Fatalf("open %d: %v %+v", i, err, got)
		}
	}
	if _, err := repo.TryOpen(ctx, s.ID); !errors.Is(err, repository.ErrShareUnavailable) {
		t.Fatalf("third open: want ErrShareUnavailable, got %v", err)
	}

	// expired share cannot be opened
	expired, err := repo.Create(ctx, repository.CreateShareParams{
		DocumentID: d.ID, RecipientEmail: "bob@example.com", TokenHash: "hash-2",
		ExpiresAt: time.Now().Add(-time.Hour), MaxOpens: 5,
	})
	if err != nil {
		t.Fatalf("Create expired: %v", err)
	}
	if _, err := repo.TryOpen(ctx, expired.ID); !errors.Is(err, repository.ErrShareUnavailable) {
		t.Fatalf("expired share: want ErrShareUnavailable, got %v", err)
	}

	// only the document owner can revoke; a revoked share cannot be opened
	fresh, err := repo.Create(ctx, repository.CreateShareParams{
		DocumentID: d.ID, RecipientEmail: "bob@example.com", TokenHash: "hash-3",
		ExpiresAt: time.Now().Add(time.Hour), MaxOpens: 5,
	})
	if err != nil {
		t.Fatalf("Create fresh: %v", err)
	}
	if err := repo.Revoke(ctx, fresh.ID, other.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("non-owner revoke: want ErrNotFound, got %v", err)
	}
	if err := repo.Revoke(ctx, fresh.ID, owner.ID); err != nil {
		t.Fatalf("owner revoke: %v", err)
	}
	if _, err := repo.TryOpen(ctx, fresh.ID); !errors.Is(err, repository.ErrShareUnavailable) {
		t.Fatalf("revoked share: want ErrShareUnavailable, got %v", err)
	}

	if shares, err := repo.ListByDocument(ctx, d.ID); err != nil || len(shares) != 3 {
		t.Fatalf("ListByDocument: %v len=%d", err, len(shares))
	}
}

func TestAuditRepo(t *testing.T) {
	ctx := context.Background()
	tx := newTx(t)
	repo := repository.NewAuditRepo(tx)

	owner := mustUser(t, tx, "owner@example.com")
	d := mustDoc(t, tx, owner.ID)

	events := []repository.LogEntry{
		{UserID: &owner.ID, DocumentID: &d.ID, Action: "document.download", IPAddress: "203.0.113.5", UserAgent: "test"},
		{UserID: &owner.ID, DocumentID: &d.ID, Action: "share.create"}, // no IP, no user agent
	}
	for _, e := range events {
		if err := repo.Log(ctx, e); err != nil {
			t.Fatalf("Log: %v", err)
		}
	}

	all, err := repo.ListByDocument(ctx, d.ID, repository.AuditFilter{})
	if err != nil || len(all) != 2 {
		t.Fatalf("list all: %v len=%d", err, len(all))
	}

	only, err := repo.ListByDocument(ctx, d.ID, repository.AuditFilter{Action: "document.download"})
	if err != nil || len(only) != 1 {
		t.Fatalf("filter by action: %v len=%d", err, len(only))
	}
	if only[0].IPAddress != "203.0.113.5" || only[0].Result != "success" {
		t.Fatalf("unexpected row: %+v", only[0])
	}

	future := time.Now().Add(time.Hour)
	none, err := repo.ListByDocument(ctx, d.ID, repository.AuditFilter{From: &future})
	if err != nil || len(none) != 0 {
		t.Fatalf("date filter: %v len=%d", err, len(none))
	}

	if err := repo.AnonymizeUser(ctx, owner.ID); err != nil {
		t.Fatalf("AnonymizeUser: %v", err)
	}
	after, _ := repo.ListByDocument(ctx, d.ID, repository.AuditFilter{})
	if len(after) != 2 || after[0].UserID != nil {
		t.Fatalf("audit rows must remain but lose the user link: %+v", after)
	}
}
