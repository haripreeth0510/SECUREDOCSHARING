package model

import "time"

type User struct {
	ID           int       `json:"id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

type Document struct {
	ID         int        `json:"id"`
	OwnerID    int        `json:"owner_id"`
	Name       string     `json:"name"`
	SizeBytes  int64      `json:"size_bytes"`
	StorageURL string     `json:"storage_url"`
	CreatedAt  time.Time  `json:"created_at"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty"`
}

type DocumentShare struct {
	ID             int        `json:"id"`
	DocumentID     int        `json:"document_id"`
	RecipientEmail string     `json:"recipient_email"`
	ShareToken     string     `json:"share_token"`
	ExpiresAt      time.Time  `json:"expires_at"`
	OpenCount      int        `json:"open_count"`
	MaxOpens       int        `json:"max_opens"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
}

type AuditLog struct {
	ID         int       `json:"id"`
	DocumentID *int      `json:"document_id,omitempty"`
	UserID     *int      `json:"user_id,omitempty"`
	Action     string    `json:"action"`
	IPAddress  string    `json:"ip_address"`
	CreatedAt  time.Time `json:"created_at"`
}
