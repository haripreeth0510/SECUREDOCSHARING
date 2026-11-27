package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/securedocsharing/app/internal/model"
)

type UserRepository interface {
	Create(ctx context.Context, email, passwordHash string, termsAcceptedAt time.Time) (*model.User, error)
	// CreateWithConsent inserts the user and their terms-acceptance record in one
	// atomic statement, so there is never a user without a consent log entry.
	CreateWithConsent(ctx context.Context, email, passwordHash, termsVersion string, acceptedAt time.Time) (*model.User, error)
	UpdatePasswordHash(ctx context.Context, id, passwordHash string) error
	FindByID(ctx context.Context, id string) (*model.User, error)
	FindByEmail(ctx context.Context, email string) (*model.User, error)
	Delete(ctx context.Context, id string) error
}

type userRepo struct{ db DBTX }

func NewUserRepo(db DBTX) UserRepository { return &userRepo{db: db} }

const userCols = `id::text, email::text, password_hash, terms_accepted_at, created_at`

func scanUser(row pgx.Row) (*model.User, error) {
	var u model.User
	if err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.TermsAcceptedAt, &u.CreatedAt); err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *userRepo) Create(ctx context.Context, email, passwordHash string, termsAcceptedAt time.Time) (*model.User, error) {
	u, err := scanUser(r.db.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, terms_accepted_at)
		 VALUES ($1, $2, $3)
		 RETURNING `+userCols,
		email, passwordHash, termsAcceptedAt,
	))
	if err != nil {
		return nil, fmt.Errorf("create user: %w", mapErr(err))
	}
	return u, nil
}

func (r *userRepo) CreateWithConsent(ctx context.Context, email, passwordHash, termsVersion string, acceptedAt time.Time) (*model.User, error) {
	u, err := scanUser(r.db.QueryRow(ctx,
		`WITH u AS (
		     INSERT INTO users (email, password_hash, terms_accepted_at)
		     VALUES ($1, $2, $3)
		     -- No error on duplicates: an error would abort the caller's
		     -- transaction. Zero rows back means the email is taken.
		     ON CONFLICT (email) DO NOTHING
		     RETURNING *
		 ), c AS (
		     INSERT INTO consent_log (user_id, document_name, version, accepted_at)
		     SELECT id, 'terms', $4, $3 FROM u
		 )
		 SELECT `+userCols+` FROM u`,
		email, passwordHash, acceptedAt, termsVersion,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("create user with consent: %w", ErrConflict)
	}
	if err != nil {
		return nil, fmt.Errorf("create user with consent: %w", mapErr(err))
	}
	return u, nil
}

func (r *userRepo) UpdatePasswordHash(ctx context.Context, id, passwordHash string) error {
	if !IsUUID(id) {
		return fmt.Errorf("update password hash: %w", ErrNotFound)
	}
	tag, err := r.db.Exec(ctx,
		`UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1::uuid`, id, passwordHash)
	if err != nil {
		return fmt.Errorf("update password hash: %w", mapErr(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("update password hash: %w", ErrNotFound)
	}
	return nil
}

func (r *userRepo) FindByID(ctx context.Context, id string) (*model.User, error) {
	if !IsUUID(id) {
		return nil, fmt.Errorf("find user by id: %w", ErrNotFound)
	}
	u, err := scanUser(r.db.QueryRow(ctx,
		`SELECT `+userCols+` FROM users WHERE id = $1::uuid`, id))
	if err != nil {
		return nil, fmt.Errorf("find user by id: %w", mapErr(err))
	}
	return u, nil
}

// FindByEmail is case-insensitive because the column is citext.
func (r *userRepo) FindByEmail(ctx context.Context, email string) (*model.User, error) {
	u, err := scanUser(r.db.QueryRow(ctx,
		`SELECT `+userCols+` FROM users WHERE email = $1`, email))
	if err != nil {
		return nil, fmt.Errorf("find user by email: %w", mapErr(err))
	}
	return u, nil
}

func (r *userRepo) Delete(ctx context.Context, id string) error {
	if !IsUUID(id) {
		return fmt.Errorf("delete user: %w", ErrNotFound)
	}
	tag, err := r.db.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", mapErr(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("delete user: %w", ErrNotFound)
	}
	return nil
}
