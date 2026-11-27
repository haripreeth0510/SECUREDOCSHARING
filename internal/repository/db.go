package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is satisfied by *pgxpool.Pool and pgx.Tx. The app passes the pool;
// tests pass a transaction that is rolled back, so nothing is left behind.
// Begin starts a transaction on a pool, or a savepoint inside a transaction.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

var (
	ErrNotFound         = errors.New("not found")
	ErrConflict         = errors.New("already exists")
	ErrShareUnavailable = errors.New("share is expired, revoked or fully used")
	// ErrTokenReused means an already-rotated refresh token was presented again,
	// which signals that it was stolen.
	ErrTokenReused = errors.New("refresh token reused")
)

// mapErr turns driver errors into the sentinel errors above.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return ErrConflict
		case "22P02": // invalid_text_representation, e.g. a malformed UUID
			return ErrNotFound
		}
	}
	return err
}

// IsUUID reports whether s is a canonical 36-character UUID. Repositories check
// IDs with it before querying: a malformed UUID cast fails inside Postgres and
// aborts the surrounding transaction, so it must never reach the database.
func IsUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return true
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 200 {
		return 200
	}
	return limit
}

func clampOffset(offset int) int {
	if offset < 0 {
		return 0
	}
	return offset
}
