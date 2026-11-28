package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/securedocsharing/app/internal/model"
)

// CreateDocumentParams carries everything the documents table requires,
// including the envelope-encryption metadata.
type CreateDocumentParams struct {
	OwnerID      string
	Name         string
	ContentType  string
	SizeBytes    int64
	StorageURL   string
	EncAlgorithm string
	WrappedDEK   []byte
	KEKID        string
	Nonce        []byte
}

type DocumentRepository interface {
	Create(ctx context.Context, p CreateDocumentParams) (*model.Document, error)
	// FindByID returns the full record including encryption metadata.
	FindByID(ctx context.Context, id string) (*model.Document, error)
	// FindByIDForOwner is the safe lookup for user-facing requests (prevents access to other users' documents).
	FindByIDForOwner(ctx context.Context, id, ownerID string) (*model.Document, error)
	ListByOwner(ctx context.Context, ownerID string, limit, offset int) ([]*model.Document, error)
	SoftDelete(ctx context.Context, id, ownerID string) error
	// DeleteAllByOwner removes rows only. The service must delete blobs and keys first.
	DeleteAllByOwner(ctx context.Context, ownerID string) error
}

type documentRepo struct{ db DBTX }

func NewDocumentRepo(db DBTX) DocumentRepository { return &documentRepo{db: db} }

const docFullCols = `id::text, owner_id::text, name, content_type, size_bytes, storage_url,
	enc_algorithm, wrapped_dek, kek_id, nonce, created_at, deleted_at`

const docMetaCols = `id::text, owner_id::text, name, content_type, size_bytes, created_at, deleted_at`

func scanDocumentFull(row pgx.Row) (*model.Document, error) {
	var d model.Document
	err := row.Scan(&d.ID, &d.OwnerID, &d.Name, &d.ContentType, &d.SizeBytes, &d.StorageURL,
		&d.EncAlgorithm, &d.WrappedDEK, &d.KEKID, &d.Nonce, &d.CreatedAt, &d.DeletedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (r *documentRepo) Create(ctx context.Context, p CreateDocumentParams) (*model.Document, error) {
	if p.EncAlgorithm == "" {
		p.EncAlgorithm = "AES-256-GCM"
	}
	d, err := scanDocumentFull(r.db.QueryRow(ctx,
		`INSERT INTO documents (owner_id, name, content_type, size_bytes, storage_url,
		                        enc_algorithm, wrapped_dek, kek_id, nonce)
		 VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING `+docFullCols,
		p.OwnerID, p.Name, p.ContentType, p.SizeBytes, p.StorageURL,
		p.EncAlgorithm, p.WrappedDEK, p.KEKID, p.Nonce,
	))
	if err != nil {
		return nil, fmt.Errorf("create document: %w", mapErr(err))
	}
	return d, nil
}

func (r *documentRepo) FindByID(ctx context.Context, id string) (*model.Document, error) {
	if !IsUUID(id) {
		return nil, fmt.Errorf("find document: %w", ErrNotFound)
	}
	d, err := scanDocumentFull(r.db.QueryRow(ctx,
		`SELECT `+docFullCols+` FROM documents WHERE id = $1::uuid AND deleted_at IS NULL`, id))
	if err != nil {
		return nil, fmt.Errorf("find document: %w", mapErr(err))
	}
	return d, nil
}

func (r *documentRepo) FindByIDForOwner(ctx context.Context, id, ownerID string) (*model.Document, error) {
	if !IsUUID(id) || !IsUUID(ownerID) {
		return nil, fmt.Errorf("find document for owner: %w", ErrNotFound)
	}
	d, err := scanDocumentFull(r.db.QueryRow(ctx,
		`SELECT `+docFullCols+` FROM documents
		 WHERE id = $1::uuid AND owner_id = $2::uuid AND deleted_at IS NULL`, id, ownerID))
	if err != nil {
		return nil, fmt.Errorf("find document for owner: %w", mapErr(err))
	}
	return d, nil
}

func (r *documentRepo) ListByOwner(ctx context.Context, ownerID string, limit, offset int) ([]*model.Document, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+docMetaCols+` FROM documents
		 WHERE owner_id = $1::uuid AND deleted_at IS NULL
		 ORDER BY created_at DESC, id LIMIT $2 OFFSET $3`,
		ownerID, clampLimit(limit), clampOffset(offset))
	if err != nil {
		return nil, fmt.Errorf("list documents: %w", mapErr(err))
	}
	defer rows.Close()

	docs := make([]*model.Document, 0)
	for rows.Next() {
		var d model.Document
		if err := rows.Scan(&d.ID, &d.OwnerID, &d.Name, &d.ContentType, &d.SizeBytes, &d.CreatedAt, &d.DeletedAt); err != nil {
			return nil, fmt.Errorf("scan document: %w", err)
		}
		docs = append(docs, &d)
	}
	return docs, rows.Err()
}

func (r *documentRepo) SoftDelete(ctx context.Context, id, ownerID string) error {
	if !IsUUID(id) || !IsUUID(ownerID) {
		return fmt.Errorf("soft delete document: %w", ErrNotFound)
	}
	tag, err := r.db.Exec(ctx,
		`UPDATE documents SET deleted_at = now()
		 WHERE id = $1::uuid AND owner_id = $2::uuid AND deleted_at IS NULL`, id, ownerID)
	if err != nil {
		return fmt.Errorf("soft delete document: %w", mapErr(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("soft delete document: %w", ErrNotFound)
	}
	return nil
}

func (r *documentRepo) DeleteAllByOwner(ctx context.Context, ownerID string) error {
	if _, err := r.db.Exec(ctx, `DELETE FROM documents WHERE owner_id = $1::uuid`, ownerID); err != nil {
		return fmt.Errorf("delete documents by owner: %w", mapErr(err))
	}
	return nil
}
