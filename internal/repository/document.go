package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/securedocsharing/app/internal/model"
)

type DocumentRepository interface {
	Create(ctx context.Context, ownerID int, name, storageURL string, sizeBytes int64) (*model.Document, error)
	FindByID(ctx context.Context, id int) (*model.Document, error)
	ListByOwner(ctx context.Context, ownerID, limit, offset int) ([]*model.Document, error)
	SoftDelete(ctx context.Context, id int) error
	DeleteAllByOwner(ctx context.Context, ownerID int) error
}

type documentRepo struct{ db *pgxpool.Pool }

func NewDocumentRepo(db *pgxpool.Pool) DocumentRepository {
	return &documentRepo{db: db}
}

func (r *documentRepo) Create(ctx context.Context, ownerID int, name, storageURL string, sizeBytes int64) (*model.Document, error) {
	var d model.Document
	err := r.db.QueryRow(ctx,
		`INSERT INTO documents (owner_id, name, storage_url, size_bytes)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, owner_id, name, size_bytes, storage_url, created_at, deleted_at`,
		ownerID, name, storageURL, sizeBytes,
	).Scan(&d.ID, &d.OwnerID, &d.Name, &d.SizeBytes, &d.StorageURL, &d.CreatedAt, &d.DeletedAt)
	if err != nil {
		return nil, fmt.Errorf("create document: %w", err)
	}
	return &d, nil
}

func (r *documentRepo) FindByID(ctx context.Context, id int) (*model.Document, error) {
	var d model.Document
	err := r.db.QueryRow(ctx,
		`SELECT id, owner_id, name, size_bytes, storage_url, created_at, deleted_at
		 FROM documents WHERE id = $1 AND deleted_at IS NULL`,
		id,
	).Scan(&d.ID, &d.OwnerID, &d.Name, &d.SizeBytes, &d.StorageURL, &d.CreatedAt, &d.DeletedAt)
	if err != nil {
		return nil, fmt.Errorf("find document: %w", err)
	}
	return &d, nil
}

func (r *documentRepo) ListByOwner(ctx context.Context, ownerID, limit, offset int) ([]*model.Document, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, owner_id, name, size_bytes, storage_url, created_at, deleted_at
		 FROM documents WHERE owner_id = $1 AND deleted_at IS NULL
		 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		ownerID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("list documents: %w", err)
	}
	defer rows.Close()

	var docs []*model.Document
	for rows.Next() {
		var d model.Document
		if err := rows.Scan(&d.ID, &d.OwnerID, &d.Name, &d.SizeBytes, &d.StorageURL, &d.CreatedAt, &d.DeletedAt); err != nil {
			return nil, fmt.Errorf("scan document: %w", err)
		}
		docs = append(docs, &d)
	}
	return docs, rows.Err()
}

func (r *documentRepo) SoftDelete(ctx context.Context, id int) error {
	_, err := r.db.Exec(ctx,
		`UPDATE documents SET deleted_at = NOW() WHERE id = $1 AND deleted_at IS NULL`, id,
	)
	if err != nil {
		return fmt.Errorf("soft delete document: %w", err)
	}
	return nil
}

func (r *documentRepo) DeleteAllByOwner(ctx context.Context, ownerID int) error {
	_, err := r.db.Exec(ctx, `DELETE FROM documents WHERE owner_id = $1`, ownerID)
	if err != nil {
		return fmt.Errorf("delete all documents: %w", err)
	}
	return nil
}
