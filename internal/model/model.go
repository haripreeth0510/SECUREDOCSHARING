package model

import "time"

// IDs are UUIDs carried as strings (the database column type is uuid).

type User struct {
	ID              string    `json:"id"`
	Email           string    `json:"email"`
	PasswordHash    string    `json:"-"`
	TermsAcceptedAt time.Time `json:"terms_accepted_at"`
	CreatedAt       time.Time `json:"created_at"`
}

// Document holds file metadata. Encryption fields never leave the server.
type Document struct {
	ID           string     `json:"id"`
	OwnerID      string     `json:"owner_id"`
	Name         string     `json:"name"`
	ContentType  string     `json:"content_type"`
	SizeBytes    int64      `json:"size_bytes"`
	StorageURL   string     `json:"-"`
	EncAlgorithm string     `json:"-"`
	WrappedDEK   []byte     `json:"-"`
	KEKID        string     `json:"-"`
	Nonce        []byte     `json:"-"`
	CreatedAt    time.Time  `json:"created_at"`
	DeletedAt    *time.Time `json:"deleted_at,omitempty"`
}

// DocumentShare never carries the raw token; only its hash is stored.
type DocumentShare struct {
	ID             string     `json:"id"`
	DocumentID     string     `json:"document_id"`
	RecipientEmail string     `json:"recipient_email"`
	TokenHash      string     `json:"-"`
	ExpiresAt      time.Time  `json:"expires_at"`
	OpenCount      int        `json:"open_count"`
	MaxOpens       int        `json:"max_opens"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

type AuditLog struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	UserID     *string   `json:"user_id,omitempty"`
	DocumentID *string   `json:"document_id,omitempty"`
	ShareID    *string   `json:"share_id,omitempty"`
	Action     string    `json:"action"`
	Result     string    `json:"result"`
	IPAddress  string    `json:"ip_address"`
	UserAgent  string    `json:"user_agent,omitempty"`
}
