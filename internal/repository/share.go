package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/securedocsharing/app/internal/model"
)

type ShareRepository interface {
	Create(ctx context.Context, documentID int, recipientEmail, shareToken string, expiresAt time.Time, maxOpens int) (*model.DocumentShare, error)
	FindByToken(ctx context.Context, token string) (*model.DocumentShare, error)
	ListByDocument(ctx context.Context, documentID int) ([]*model.DocumentShare, error)
	IncrementOpenCount(ctx context.Context, id int) error
	Revoke(ctx context.Context, id int) error
	DeleteAllByDocument(ctx context.Context, documentID int) error
}

type shareRepo struct{ db *pgxpool.Pool }

func NewShareRepo(db *pgxpool.Pool) ShareRepository {
	return &shareRepo{db: db}
}

func (r *shareRepo) Create(ctx context.Context, documentID int, recipientEmail, shareToken string, expiresAt time.Time, maxOpens int) (*model.DocumentShare, error) {
	var s model.DocumentShare
	err := r.db.QueryRow(ctx,
		`INSERT INTO document_shares (document_id, recipient_email, share_token, expires_at, max_opens)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, document_id, recipient_email, share_token, expires_at, open_count, max_opens, revoked_at`,
		documentID, recipientEmail, shareToken, expiresAt, maxOpens,
	).Scan(&s.ID, &s.DocumentID, &s.RecipientEmail, &s.ShareToken, &s.ExpiresAt, &s.OpenCount, &s.MaxOpens, &s.RevokedAt)
	if err != nil {
		return nil, fmt.Errorf("create share: %w", err)
	}
	return &s, nil
}

func (r *shareRepo) FindByToken(ctx context.Context, token string) (*model.DocumentShare, error) {
	var s model.DocumentShare
	err := r.db.QueryRow(ctx,
		`SELECT id, document_id, recipient_email, share_token, expires_at, open_count, max_opens, revoked_at
		 FROM document_shares WHERE share_token = $1`,
		token,
	).Scan(&s.ID, &s.DocumentID, &s.RecipientEmail, &s.ShareToken, &s.ExpiresAt, &s.OpenCount, &s.MaxOpens, &s.RevokedAt)
	if err != nil {
		return nil, fmt.Errorf("find share by token: %w", err)
	}
	return &s, nil
}

func (r *shareRepo) ListByDocument(ctx context.Context, documentID int) ([]*model.DocumentShare, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, document_id, recipient_email, share_token, expires_at, open_count, max_opens, revoked_at
		 FROM document_shares WHERE document_id = $1 ORDER BY id DESC`,
		documentID,
	)
	if err != nil {
		return nil, fmt.Errorf("list shares: %w", err)
	}
	defer rows.Close()

	var shares []*model.DocumentShare
	for rows.Next() {
		var s model.DocumentShare
		if err := rows.Scan(&s.ID, &s.DocumentID, &s.RecipientEmail, &s.ShareToken, &s.ExpiresAt, &s.OpenCount, &s.MaxOpens, &s.RevokedAt); err != nil {
			return nil, fmt.Errorf("scan share: %w", err)
		}
		shares = append(shares, &s)
	}
	return shares, rows.Err()
}

func (r *shareRepo) IncrementOpenCount(ctx context.Context, id int) error {
	_, err := r.db.Exec(ctx, `UPDATE document_shares SET open_count = open_count + 1 WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("increment open count: %w", err)
	}
	return nil
}

func (r *shareRepo) Revoke(ctx context.Context, id int) error {
	_, err := r.db.Exec(ctx, `UPDATE document_shares SET revoked_at = NOW() WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("revoke share: %w", err)
	}
	return nil
}

func (r *shareRepo) DeleteAllByDocument(ctx context.Context, documentID int) error {
	_, err := r.db.Exec(ctx, `DELETE FROM document_shares WHERE document_id = $1`, documentID)
	if err != nil {
		return fmt.Errorf("delete shares by document: %w", err)
	}
	return nil
}
