package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/securedocsharing/app/internal/model"
)

type AuditRepository interface {
	Log(ctx context.Context, documentID *int, userID *int, action, ipAddress string) error
	ListByDocument(ctx context.Context, documentID, limit, offset int) ([]*model.AuditLog, error)
	ListByDocumentFiltered(ctx context.Context, documentID int, action string, from, to time.Time, limit, offset int) ([]*model.AuditLog, error)
	DeleteAllByUser(ctx context.Context, userID int) error
}

type auditRepo struct{ db *pgxpool.Pool }

func NewAuditRepo(db *pgxpool.Pool) AuditRepository {
	return &auditRepo{db: db}
}

func (r *auditRepo) Log(ctx context.Context, documentID *int, userID *int, action, ipAddress string) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO audit_logs (document_id, user_id, action, ip_address) VALUES ($1, $2, $3, $4)`,
		documentID, userID, action, ipAddress,
	)
	if err != nil {
		return fmt.Errorf("insert audit log: %w", err)
	}
	return nil
}

func (r *auditRepo) ListByDocument(ctx context.Context, documentID, limit, offset int) ([]*model.AuditLog, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, document_id, user_id, action, ip_address, created_at
		 FROM audit_logs WHERE document_id = $1
		 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		documentID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("list audit logs: %w", err)
	}
	defer rows.Close()

	var logs []*model.AuditLog
	for rows.Next() {
		var l model.AuditLog
		if err := rows.Scan(&l.ID, &l.DocumentID, &l.UserID, &l.Action, &l.IPAddress, &l.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan audit log: %w", err)
		}
		logs = append(logs, &l)
	}
	return logs, rows.Err()
}

func (r *auditRepo) ListByDocumentFiltered(ctx context.Context, documentID int, action string, from, to time.Time, limit, offset int) ([]*model.AuditLog, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, document_id, user_id, action, ip_address, created_at
		 FROM audit_logs
		 WHERE document_id = $1 AND action = $2 AND created_at BETWEEN $3 AND $4
		 ORDER BY created_at DESC LIMIT $5 OFFSET $6`,
		documentID, action, from, to, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("list filtered audit logs: %w", err)
	}
	defer rows.Close()

	var logs []*model.AuditLog
	for rows.Next() {
		var l model.AuditLog
		if err := rows.Scan(&l.ID, &l.DocumentID, &l.UserID, &l.Action, &l.IPAddress, &l.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan audit log: %w", err)
		}
		logs = append(logs, &l)
	}
	return logs, rows.Err()
}

func (r *auditRepo) DeleteAllByUser(ctx context.Context, userID int) error {
	_, err := r.db.Exec(ctx, `DELETE FROM audit_logs WHERE user_id = $1`, userID)
	if err != nil {
		return fmt.Errorf("delete audit logs by user: %w", err)
	}
	return nil
}
