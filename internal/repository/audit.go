package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/securedocsharing/app/internal/model"
)

// LogEntry is one audit event. Result defaults to "success".
type LogEntry struct {
	UserID     *string
	DocumentID *string
	ShareID    *string
	Action     string
	Result     string
	IPAddress  string
	UserAgent  string
}

// AuditFilter narrows a listing. Zero values mean "no filter".
type AuditFilter struct {
	Action string
	From   *time.Time // inclusive
	To     *time.Time // exclusive
	Limit  int
	Offset int
}

type AuditRepository interface {
	Log(ctx context.Context, e LogEntry) error
	ListByDocument(ctx context.Context, documentID string, f AuditFilter) ([]*model.AuditLog, error)
	// AnonymizeUser removes the link between a deleted user and their audit rows
	// (GDPR) without deleting the audit trail itself.
	AnonymizeUser(ctx context.Context, userID string) error
}

type auditRepo struct{ db DBTX }

func NewAuditRepo(db DBTX) AuditRepository { return &auditRepo{db: db} }

func (r *auditRepo) Log(ctx context.Context, e LogEntry) error {
	if e.Result == "" {
		e.Result = "success"
	}
	_, err := r.db.Exec(ctx,
		`INSERT INTO audit_logs (user_id, document_id, share_id, action, result, ip_address, user_agent)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, NULLIF($6, '')::inet, NULLIF($7, ''))`,
		e.UserID, e.DocumentID, e.ShareID, e.Action, e.Result, e.IPAddress, e.UserAgent,
	)
	if err != nil {
		return fmt.Errorf("insert audit log: %w", mapErr(err))
	}
	return nil
}

func (r *auditRepo) ListByDocument(ctx context.Context, documentID string, f AuditFilter) ([]*model.AuditLog, error) {
	if !IsUUID(documentID) {
		return nil, fmt.Errorf("list audit logs: %w", ErrNotFound)
	}
	rows, err := r.db.Query(ctx,
		`SELECT id::text, created_at, user_id::text, document_id::text, share_id::text,
		        action, result, COALESCE(host(ip_address), ''), COALESCE(user_agent, '')
		 FROM audit_logs
		 WHERE document_id = $1::uuid
		   AND ($2::text = '' OR action = $2::text)
		   AND ($3::timestamptz IS NULL OR created_at >= $3::timestamptz)
		   AND ($4::timestamptz IS NULL OR created_at <  $4::timestamptz)
		 ORDER BY created_at DESC, id
		 LIMIT $5 OFFSET $6`,
		documentID, f.Action, f.From, f.To, clampLimit(f.Limit), clampOffset(f.Offset),
	)
	if err != nil {
		return nil, fmt.Errorf("list audit logs: %w", mapErr(err))
	}
	defer rows.Close()

	logs := make([]*model.AuditLog, 0)
	for rows.Next() {
		var l model.AuditLog
		if err := rows.Scan(&l.ID, &l.CreatedAt, &l.UserID, &l.DocumentID, &l.ShareID,
			&l.Action, &l.Result, &l.IPAddress, &l.UserAgent); err != nil {
			return nil, fmt.Errorf("scan audit log: %w", err)
		}
		logs = append(logs, &l)
	}
	return logs, rows.Err()
}

func (r *auditRepo) AnonymizeUser(ctx context.Context, userID string) error {
	if _, err := r.db.Exec(ctx, `UPDATE audit_logs SET user_id = NULL WHERE user_id = $1::uuid`, userID); err != nil {
		return fmt.Errorf("anonymize audit logs: %w", mapErr(err))
	}
	return nil
}
