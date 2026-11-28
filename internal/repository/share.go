package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/securedocsharing/app/internal/model"
)

// CreateShareParams takes the HASH of the share token. The service generates
// the raw token, shows it to the sender once, and stores only its hash.
type CreateShareParams struct {
	DocumentID     string
	RecipientEmail string
	TokenHash      string
	ExpiresAt      time.Time
	MaxOpens       int
}

type ShareRepository interface {
	Create(ctx context.Context, p CreateShareParams) (*model.DocumentShare, error)
	FindByTokenHash(ctx context.Context, tokenHash string) (*model.DocumentShare, error)
	ListByDocument(ctx context.Context, documentID string) ([]*model.DocumentShare, error)
	// TryOpen atomically counts one open. It fails with ErrShareUnavailable if the
	// share is expired, revoked or already used up, even under concurrent requests.
	TryOpen(ctx context.Context, id string) (*model.DocumentShare, error)
	// Revoke only succeeds if the share belongs to a document owned by ownerID.
	Revoke(ctx context.Context, id, ownerID string) error
	DeleteAllByDocument(ctx context.Context, documentID string) error
}

type shareRepo struct{ db DBTX }

func NewShareRepo(db DBTX) ShareRepository { return &shareRepo{db: db} }

const shareCols = `id::text, document_id::text, recipient_email::text, token_hash,
	expires_at, open_count, max_opens, revoked_at, created_at`

func scanShare(row pgx.Row) (*model.DocumentShare, error) {
	var s model.DocumentShare
	err := row.Scan(&s.ID, &s.DocumentID, &s.RecipientEmail, &s.TokenHash,
		&s.ExpiresAt, &s.OpenCount, &s.MaxOpens, &s.RevokedAt, &s.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *shareRepo) Create(ctx context.Context, p CreateShareParams) (*model.DocumentShare, error) {
	if p.MaxOpens <= 0 {
		p.MaxOpens = 1
	}
	s, err := scanShare(r.db.QueryRow(ctx,
		`INSERT INTO document_shares (document_id, recipient_email, token_hash, expires_at, max_opens)
		 VALUES ($1::uuid, $2, $3, $4, $5)
		 RETURNING `+shareCols,
		p.DocumentID, p.RecipientEmail, p.TokenHash, p.ExpiresAt, p.MaxOpens,
	))
	if err != nil {
		return nil, fmt.Errorf("create share: %w", mapErr(err))
	}
	return s, nil
}

func (r *shareRepo) FindByTokenHash(ctx context.Context, tokenHash string) (*model.DocumentShare, error) {
	s, err := scanShare(r.db.QueryRow(ctx,
		`SELECT `+shareCols+` FROM document_shares WHERE token_hash = $1`, tokenHash))
	if err != nil {
		return nil, fmt.Errorf("find share by token: %w", mapErr(err))
	}
	return s, nil
}

func (r *shareRepo) ListByDocument(ctx context.Context, documentID string) ([]*model.DocumentShare, error) {
	if !IsUUID(documentID) {
		return nil, fmt.Errorf("list shares: %w", ErrNotFound)
	}
	rows, err := r.db.Query(ctx,
		`SELECT `+shareCols+` FROM document_shares
		 WHERE document_id = $1::uuid ORDER BY created_at DESC, id`, documentID)
	if err != nil {
		return nil, fmt.Errorf("list shares: %w", mapErr(err))
	}
	defer rows.Close()

	shares := make([]*model.DocumentShare, 0)
	for rows.Next() {
		s, err := scanShare(rows)
		if err != nil {
			return nil, fmt.Errorf("scan share: %w", err)
		}
		shares = append(shares, s)
	}
	return shares, rows.Err()
}

func (r *shareRepo) TryOpen(ctx context.Context, id string) (*model.DocumentShare, error) {
	if !IsUUID(id) {
		return nil, ErrShareUnavailable
	}
	s, err := scanShare(r.db.QueryRow(ctx,
		`UPDATE document_shares
		 SET open_count = open_count + 1
		 WHERE id = $1::uuid
		   AND revoked_at IS NULL
		   AND expires_at > now()
		   AND open_count < max_opens
		 RETURNING `+shareCols, id))
	if err != nil {
		if mapped := mapErr(err); mapped == ErrNotFound {
			return nil, ErrShareUnavailable
		}
		return nil, fmt.Errorf("open share: %w", err)
	}
	return s, nil
}

func (r *shareRepo) Revoke(ctx context.Context, id, ownerID string) error {
	if !IsUUID(id) || !IsUUID(ownerID) {
		return fmt.Errorf("revoke share: %w", ErrNotFound)
	}
	tag, err := r.db.Exec(ctx,
		`UPDATE document_shares s SET revoked_at = now()
		 FROM documents d
		 WHERE s.id = $1::uuid AND s.document_id = d.id
		   AND d.owner_id = $2::uuid AND s.revoked_at IS NULL`, id, ownerID)
	if err != nil {
		return fmt.Errorf("revoke share: %w", mapErr(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("revoke share: %w", ErrNotFound)
	}
	return nil
}

func (r *shareRepo) DeleteAllByDocument(ctx context.Context, documentID string) error {
	if _, err := r.db.Exec(ctx, `DELETE FROM document_shares WHERE document_id = $1::uuid`, documentID); err != nil {
		return fmt.Errorf("delete shares by document: %w", mapErr(err))
	}
	return nil
}
