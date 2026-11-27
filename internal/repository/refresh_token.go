package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// RefreshTokenRepository stores refresh tokens by hash only.
type RefreshTokenRepository interface {
	Create(ctx context.Context, userID, tokenHash string, expiresAt time.Time) error
	// Rotate revokes the live token oldHash and stores newHash in its place,
	// returning the owner's user ID. It returns ErrNotFound if oldHash is
	// unknown, expired or was revoked by logout, and ErrTokenReused (with the
	// user ID) if oldHash was already rotated: the caller should then revoke
	// every session of that user.
	Rotate(ctx context.Context, oldHash, newHash string, newExpiresAt time.Time) (userID string, err error)
	// Revoke revokes one live token and returns its owner. ErrNotFound if it
	// is unknown or already revoked.
	Revoke(ctx context.Context, tokenHash string) (userID string, err error)
	RevokeAllForUser(ctx context.Context, userID string) error
}

type refreshTokenRepo struct{ db DBTX }

func NewRefreshTokenRepo(db DBTX) RefreshTokenRepository { return &refreshTokenRepo{db: db} }

func (r *refreshTokenRepo) Create(ctx context.Context, userID, tokenHash string, expiresAt time.Time) error {
	if !IsUUID(userID) {
		return fmt.Errorf("create refresh token: %w", ErrNotFound)
	}
	_, err := r.db.Exec(ctx,
		`INSERT INTO refresh_tokens (user_id, token_hash, expires_at) VALUES ($1::uuid, $2, $3)`,
		userID, tokenHash, expiresAt)
	if err != nil {
		return fmt.Errorf("create refresh token: %w", mapErr(err))
	}
	return nil
}

func (r *refreshTokenRepo) Rotate(ctx context.Context, oldHash, newHash string, newExpiresAt time.Time) (string, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("rotate refresh token: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// FOR UPDATE serialises concurrent refreshes of the same token: the second
	// one waits, then sees the token already rotated and is treated as reuse.
	var (
		id, userID string
		revoked    bool
		rotated    bool
		live       bool
	)
	err = tx.QueryRow(ctx,
		`SELECT id::text, user_id::text, revoked_at IS NOT NULL, replaced_by IS NOT NULL, expires_at > now()
		 FROM refresh_tokens WHERE token_hash = $1 FOR UPDATE`, oldHash,
	).Scan(&id, &userID, &revoked, &rotated, &live)
	if err != nil {
		return "", fmt.Errorf("rotate refresh token: %w", mapErr(err))
	}
	switch {
	case rotated:
		return userID, ErrTokenReused
	case revoked || !live:
		return "", fmt.Errorf("rotate refresh token: %w", ErrNotFound)
	}

	var newID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO refresh_tokens (user_id, token_hash, expires_at)
		 VALUES ($1::uuid, $2, $3) RETURNING id::text`,
		userID, newHash, newExpiresAt,
	).Scan(&newID); err != nil {
		return "", fmt.Errorf("rotate refresh token: insert: %w", mapErr(err))
	}
	if _, err := tx.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = now(), replaced_by = $2::uuid WHERE id = $1::uuid`,
		id, newID); err != nil {
		return "", fmt.Errorf("rotate refresh token: revoke old: %w", mapErr(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("rotate refresh token: commit: %w", err)
	}
	return userID, nil
}

func (r *refreshTokenRepo) Revoke(ctx context.Context, tokenHash string) (string, error) {
	var userID string
	err := r.db.QueryRow(ctx,
		`UPDATE refresh_tokens SET revoked_at = now()
		 WHERE token_hash = $1 AND revoked_at IS NULL
		 RETURNING user_id::text`, tokenHash).Scan(&userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("revoke refresh token: %w", ErrNotFound)
		}
		return "", fmt.Errorf("revoke refresh token: %w", mapErr(err))
	}
	return userID, nil
}

func (r *refreshTokenRepo) RevokeAllForUser(ctx context.Context, userID string) error {
	if !IsUUID(userID) {
		return nil
	}
	if _, err := r.db.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = now() WHERE user_id = $1::uuid AND revoked_at IS NULL`,
		userID); err != nil {
		return fmt.Errorf("revoke all refresh tokens: %w", mapErr(err))
	}
	return nil
}
