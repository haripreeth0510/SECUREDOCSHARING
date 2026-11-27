// Package auth holds the security primitives used by the auth service:
// password hashing, access tokens, refresh tokens and the access-token denylist.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2Params are the Argon2id cost settings. Memory is in KiB.
type Argon2Params struct {
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultArgon2Params is the second recommended option of RFC 9106 §4
// (64 MiB, t=3, p=4), for machines where 2 GiB per hash is not practical.
var DefaultArgon2Params = Argon2Params{
	Memory:      64 * 1024,
	Iterations:  3,
	Parallelism: 4,
	SaltLength:  16,
	KeyLength:   32,
}

// Upper bounds accepted when decoding a stored hash, so a corrupted or
// tampered row cannot make the server allocate unbounded memory.
const (
	maxMemoryKiB  = 1024 * 1024 // 1 GiB
	maxIterations = 16
)

var ErrInvalidHash = errors.New("invalid password hash format")

// PasswordHasher hashes and verifies passwords with Argon2id. It limits how
// many hashes run at once: each one holds Params.Memory of RAM, so a burst of
// logins would otherwise be a memory-exhaustion attack.
type PasswordHasher struct {
	params Argon2Params
	sem    chan struct{}
}

// NewPasswordHasher allows at most maxConcurrent hashes at a time.
func NewPasswordHasher(params Argon2Params, maxConcurrent int) *PasswordHasher {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &PasswordHasher{params: params, sem: make(chan struct{}, maxConcurrent)}
}

func (h *PasswordHasher) acquire(ctx context.Context) error {
	select {
	case h.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *PasswordHasher) release() { <-h.sem }

// Hash returns a PHC-format string:
// $argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>
func (h *PasswordHasher) Hash(ctx context.Context, password string) (string, error) {
	salt := make([]byte, h.params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, h.params.Iterations, h.params.Memory, h.params.Parallelism, h.params.KeyLength)
	h.release()

	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, h.params.Memory, h.params.Iterations, h.params.Parallelism,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// Verify reports whether password matches encoded. It uses the parameters
// stored in encoded, so hashes made with older settings still verify.
func (h *PasswordHasher) Verify(ctx context.Context, password, encoded string) (bool, error) {
	p, salt, want, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}
	if err := h.acquire(ctx); err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, uint32(len(want)))
	h.release()
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// NeedsRehash reports whether encoded was made with different parameters
// than the hasher's current ones, so it can be upgraded at the next login.
func (h *PasswordHasher) NeedsRehash(encoded string) bool {
	p, salt, key, err := decodeHash(encoded)
	if err != nil {
		return true
	}
	return p.Memory != h.params.Memory || p.Iterations != h.params.Iterations ||
		p.Parallelism != h.params.Parallelism || uint32(len(salt)) != h.params.SaltLength ||
		uint32(len(key)) != h.params.KeyLength
}

func decodeHash(encoded string) (Argon2Params, []byte, []byte, error) {
	var p Argon2Params
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return p, nil, nil, ErrInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return p, nil, nil, ErrInvalidHash
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Iterations, &p.Parallelism); err != nil {
		return p, nil, nil, ErrInvalidHash
	}
	if p.Memory == 0 || p.Memory > maxMemoryKiB || p.Iterations == 0 || p.Iterations > maxIterations || p.Parallelism == 0 {
		return p, nil, nil, ErrInvalidHash
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return p, nil, nil, ErrInvalidHash
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil || len(key) < 16 {
		return p, nil, nil, ErrInvalidHash
	}
	p.SaltLength = uint32(len(salt))
	p.KeyLength = uint32(len(key))
	return p, salt, key, nil
}
