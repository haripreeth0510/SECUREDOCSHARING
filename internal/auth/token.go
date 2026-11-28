package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var ErrInvalidToken = errors.New("invalid or expired token")

// MinSecretLength is the minimum HS256 key size (256 bits).
const MinSecretLength = 32

// AccessClaims are the claims carried by an access token. Subject is the user ID;
// ID (jti) identifies the token so logout can deny it before it expires.
type AccessClaims struct {
	jwt.RegisteredClaims
}

// UserID returns the token subject.
func (c *AccessClaims) UserID() string { return c.Subject }

// TokenIssuer signs and verifies short-lived HS256 access tokens.
type TokenIssuer struct {
	secret   []byte
	issuer   string
	audience string
	ttl      time.Duration
	now      func() time.Time
	parser   *jwt.Parser
}

func NewTokenIssuer(secret []byte, issuer, audience string, ttl time.Duration) (*TokenIssuer, error) {
	if len(secret) < MinSecretLength {
		return nil, fmt.Errorf("JWT secret must be at least %d bytes", MinSecretLength)
	}
	if ttl <= 0 {
		return nil, errors.New("access token TTL must be positive")
	}
	return &TokenIssuer{
		secret:   secret,
		issuer:   issuer,
		audience: audience,
		ttl:      ttl,
		now:      time.Now,
		parser: jwt.NewParser(
			jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), // blocks "alg: none" and key-confusion attacks
			jwt.WithIssuer(issuer),
			jwt.WithAudience(audience),
			jwt.WithExpirationRequired(),
			jwt.WithIssuedAt(),
			jwt.WithLeeway(5*time.Second),
		),
	}, nil
}

// TTL is the lifetime of issued access tokens.
func (t *TokenIssuer) TTL() time.Duration { return t.ttl }

// Issue returns a signed access token for userID and its claims.
func (t *TokenIssuer) Issue(userID string) (string, *AccessClaims, error) {
	jti, err := randomString(16)
	if err != nil {
		return "", nil, err
	}
	now := t.now().UTC().Truncate(time.Second)
	claims := &AccessClaims{RegisteredClaims: jwt.RegisteredClaims{
		Issuer:    t.issuer,
		Subject:   userID,
		Audience:  jwt.ClaimStrings{t.audience},
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(t.ttl)),
		ID:        jti,
	}}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
	if err != nil {
		return "", nil, fmt.Errorf("sign access token: %w", err)
	}
	return signed, claims, nil
}

// Parse verifies signature, algorithm, issuer, audience and expiry.
func (t *TokenIssuer) Parse(token string) (*AccessClaims, error) {
	claims := &AccessClaims{}
	_, err := t.parser.ParseWithClaims(token, claims, func(*jwt.Token) (any, error) { return t.secret, nil })
	if err != nil || claims.Subject == "" || claims.ID == "" {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

// NewRefreshToken returns a 256-bit random, URL-safe token. Only its hash
// (HashToken) is stored server side.
func NewRefreshToken() (string, error) { return randomString(32) }

// HashToken returns the hex SHA-256 of a high-entropy token. A fast hash is
// appropriate here: the token is random, so it cannot be brute-forced the way
// a password can.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
