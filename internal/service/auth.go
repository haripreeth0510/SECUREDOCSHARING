// Package service holds business logic. Handlers translate HTTP to calls here;
// repositories translate calls here to SQL.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/securedocsharing/app/internal/auth"
	"github.com/securedocsharing/app/internal/model"
	"github.com/securedocsharing/app/internal/ratelimit"
	"github.com/securedocsharing/app/internal/repository"
)

// Audit actions written by the auth service.
const (
	ActionRegister          = "user.register"
	ActionRegisterDuplicate = "user.register_duplicate"
	ActionLogin             = "user.login"
	ActionLogout            = "user.logout"
	ActionTokenRefresh      = "token.refresh"
	ActionTokenReuse        = "token.reuse_detected"
)

const (
	MinPasswordLength = 12
	MaxPasswordLength = 128
	MaxEmailLength    = 254
)

var (
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrInvalidToken       = errors.New("invalid or expired token")
	// ErrUnavailable means a dependency the security checks rely on (Redis)
	// is down. Requests fail closed rather than skipping the check.
	ErrUnavailable = errors.New("service temporarily unavailable")
)

// ValidationError is a client input error that is safe to show to the user.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

// RateLimitError means the caller must wait RetryAfter before trying again.
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string { return "too many requests" }

// RequestMeta is the request context recorded in the audit log.
type RequestMeta struct {
	IP        string
	UserAgent string
}

// TokenPair is returned on login and refresh.
type TokenPair struct {
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time
}

// LoginLimit is the per-account login limit, on top of the per-IP limit the
// HTTP layer applies. It slows credential stuffing spread across many IPs.
type LoginLimit struct {
	Limit  int
	Window time.Duration
}

type AuthConfig struct {
	RefreshTTL   time.Duration
	TermsVersion string
	LoginLimit   LoginLimit
}

type AuthService struct {
	users     repository.UserRepository
	tokens    repository.RefreshTokenRepository
	audit     repository.AuditRepository
	hasher    *auth.PasswordHasher
	issuer    *auth.TokenIssuer
	denylist  auth.Denylist
	limiter   ratelimit.Limiter
	cfg       AuthConfig
	log       *slog.Logger
	dummyHash string
}

func NewAuthService(
	ctx context.Context,
	users repository.UserRepository,
	tokens repository.RefreshTokenRepository,
	audit repository.AuditRepository,
	hasher *auth.PasswordHasher,
	issuer *auth.TokenIssuer,
	denylist auth.Denylist,
	limiter ratelimit.Limiter,
	cfg AuthConfig,
	log *slog.Logger,
) (*AuthService, error) {
	if cfg.RefreshTTL <= 0 || cfg.TermsVersion == "" || cfg.LoginLimit.Limit <= 0 || cfg.LoginLimit.Window <= 0 {
		return nil, errors.New("auth service: incomplete config")
	}
	// Verified against when the email is unknown, so a failed login takes the
	// same time whether or not the account exists.
	dummy, err := hasher.Hash(ctx, "timing-equaliser-not-a-real-password")
	if err != nil {
		return nil, fmt.Errorf("auth service: %w", err)
	}
	return &AuthService{
		users: users, tokens: tokens, audit: audit, hasher: hasher, issuer: issuer,
		denylist: denylist, limiter: limiter, cfg: cfg, log: log, dummyHash: dummy,
	}, nil
}

// Register creates an account. To avoid revealing which emails are registered,
// it returns nil both for a new account and for an email that already exists;
// the duplicate case is only recorded in the audit log.
func (s *AuthService) Register(ctx context.Context, meta RequestMeta, email, password string, acceptTerms bool) error {
	email, err := normaliseEmail(email)
	if err != nil {
		return err
	}
	if err := validatePassword(password); err != nil {
		return err
	}
	if !acceptTerms {
		return &ValidationError{Field: "accept_terms", Message: "the terms must be accepted"}
	}

	// Hash before the insert in both paths so timing does not reveal duplicates.
	hash, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	u, err := s.users.CreateWithConsent(ctx, email, hash, s.cfg.TermsVersion, time.Now().UTC())
	if errors.Is(err, repository.ErrConflict) {
		s.record(ctx, meta, nil, ActionRegisterDuplicate, "failure")
		return nil
	}
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	s.record(ctx, meta, &u.ID, ActionRegister, "success")
	return nil
}

// Login checks credentials and issues a token pair.
func (s *AuthService) Login(ctx context.Context, meta RequestMeta, email, password string) (*TokenPair, error) {
	email, err := normaliseEmail(email)
	if err != nil {
		return nil, ErrInvalidCredentials // do not leak why
	}
	if utf8.RuneCountInString(password) > MaxPasswordLength {
		return nil, ErrInvalidCredentials
	}

	// Key on a hash so raw email addresses are not stored in Redis.
	d, err := s.limiter.Allow(ctx, "login:account:"+sha256Hex(strings.ToLower(email)),
		s.cfg.LoginLimit.Limit, s.cfg.LoginLimit.Window)
	if err != nil {
		s.log.ErrorContext(ctx, "login rate limiter unavailable", "error", err)
		return nil, ErrUnavailable
	}
	if !d.Allowed {
		s.record(ctx, meta, nil, ActionLogin, "rate_limited")
		return nil, &RateLimitError{RetryAfter: d.RetryAfter}
	}

	u, err := s.users.FindByEmail(ctx, email)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return nil, fmt.Errorf("login: %w", err)
	}
	if u == nil {
		_, _ = s.hasher.Verify(ctx, password, s.dummyHash)
		s.record(ctx, meta, nil, ActionLogin, "failure")
		return nil, ErrInvalidCredentials
	}

	ok, err := s.hasher.Verify(ctx, password, u.PasswordHash)
	if errors.Is(err, auth.ErrInvalidHash) {
		// A corrupt or placeholder hash (e.g. dev seed data) can never match.
		// Treat it as a failed login, but flag it: it needs investigating.
		s.log.ErrorContext(ctx, "stored password hash is malformed", "user_id", u.ID)
		ok, err = false, nil
	}
	if err != nil {
		return nil, fmt.Errorf("login: verify: %w", err)
	}
	if !ok {
		s.record(ctx, meta, &u.ID, ActionLogin, "failure")
		return nil, ErrInvalidCredentials
	}

	s.upgradeHash(ctx, u, password)

	pair, err := s.issuePair(ctx, u.ID)
	if err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}
	s.record(ctx, meta, &u.ID, ActionLogin, "success")
	return pair, nil
}

// Refresh rotates a refresh token and issues a new pair. Presenting an
// already-rotated token revokes every session of that user.
func (s *AuthService) Refresh(ctx context.Context, meta RequestMeta, refreshToken string) (*TokenPair, error) {
	if refreshToken == "" {
		return nil, ErrInvalidToken
	}
	newRefresh, err := auth.NewRefreshToken()
	if err != nil {
		return nil, fmt.Errorf("refresh: %w", err)
	}
	refreshExp := time.Now().UTC().Add(s.cfg.RefreshTTL)

	userID, err := s.tokens.Rotate(ctx, auth.HashToken(refreshToken), auth.HashToken(newRefresh), refreshExp)
	switch {
	case errors.Is(err, repository.ErrTokenReused):
		if rerr := s.tokens.RevokeAllForUser(ctx, userID); rerr != nil {
			s.log.ErrorContext(ctx, "revoke sessions after token reuse", "error", rerr)
		}
		s.record(ctx, meta, &userID, ActionTokenReuse, "failure")
		return nil, ErrInvalidToken
	case errors.Is(err, repository.ErrNotFound):
		s.record(ctx, meta, nil, ActionTokenRefresh, "failure")
		return nil, ErrInvalidToken
	case err != nil:
		return nil, fmt.Errorf("refresh: %w", err)
	}

	access, claims, err := s.issuer.Issue(userID)
	if err != nil {
		return nil, fmt.Errorf("refresh: %w", err)
	}
	s.record(ctx, meta, &userID, ActionTokenRefresh, "success")
	return &TokenPair{
		AccessToken:      access,
		AccessExpiresAt:  claims.ExpiresAt.Time,
		RefreshToken:     newRefresh,
		RefreshExpiresAt: refreshExp,
	}, nil
}

// Logout revokes the refresh token and, if the caller presented a valid
// access token, denies it until it expires. Either may be absent.
func (s *AuthService) Logout(ctx context.Context, meta RequestMeta, access *auth.AccessClaims, refreshToken string) error {
	var userID *string
	if refreshToken != "" {
		id, err := s.tokens.Revoke(ctx, auth.HashToken(refreshToken))
		switch {
		case err == nil:
			userID = &id
		case !errors.Is(err, repository.ErrNotFound):
			return fmt.Errorf("logout: %w", err)
		}
	}
	if access != nil {
		if err := s.denylist.Deny(ctx, access.ID, access.ExpiresAt.Time); err != nil {
			s.log.ErrorContext(ctx, "deny access token on logout", "error", err)
			return ErrUnavailable
		}
		sub := access.UserID()
		userID = &sub
	}
	if userID != nil {
		s.record(ctx, meta, userID, ActionLogout, "success")
	}
	return nil
}

// Authenticate validates an access token for a protected request.
func (s *AuthService) Authenticate(ctx context.Context, token string) (*auth.AccessClaims, error) {
	claims, err := s.issuer.Parse(token)
	if err != nil {
		return nil, ErrInvalidToken
	}
	denied, err := s.denylist.IsDenied(ctx, claims.ID)
	if err != nil {
		s.log.ErrorContext(ctx, "denylist unavailable", "error", err)
		return nil, ErrUnavailable
	}
	if denied {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

// Me returns the authenticated user.
func (s *AuthService) Me(ctx context.Context, userID string) (*model.User, error) {
	u, err := s.users.FindByID(ctx, userID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, ErrInvalidToken // account deleted after the token was issued
	}
	if err != nil {
		return nil, fmt.Errorf("me: %w", err)
	}
	return u, nil
}

func (s *AuthService) issuePair(ctx context.Context, userID string) (*TokenPair, error) {
	access, claims, err := s.issuer.Issue(userID)
	if err != nil {
		return nil, err
	}
	refresh, err := auth.NewRefreshToken()
	if err != nil {
		return nil, err
	}
	refreshExp := time.Now().UTC().Add(s.cfg.RefreshTTL)
	if err := s.tokens.Create(ctx, userID, auth.HashToken(refresh), refreshExp); err != nil {
		return nil, err
	}
	return &TokenPair{
		AccessToken:      access,
		AccessExpiresAt:  claims.ExpiresAt.Time,
		RefreshToken:     refresh,
		RefreshExpiresAt: refreshExp,
	}, nil
}

// upgradeHash re-hashes with current parameters after a successful login.
// Failure is logged, not fatal: the old hash still works.
func (s *AuthService) upgradeHash(ctx context.Context, u *model.User, password string) {
	if !s.hasher.NeedsRehash(u.PasswordHash) {
		return
	}
	h, err := s.hasher.Hash(ctx, password)
	if err == nil {
		err = s.users.UpdatePasswordHash(ctx, u.ID, h)
	}
	if err != nil {
		s.log.WarnContext(ctx, "password rehash failed", "error", err)
	}
}

// record writes an audit event. A failed audit write is logged loudly but does
// not fail the user's request; in practice it only fails when the database
// itself is down, which already fails the request.
func (s *AuthService) record(ctx context.Context, meta RequestMeta, userID *string, action, result string) {
	err := s.audit.Log(ctx, repository.LogEntry{
		UserID: userID, Action: action, Result: result, IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	if err != nil {
		s.log.ErrorContext(ctx, "audit log write failed", "action", action, "result", result, "error", err)
	}
}

func normaliseEmail(raw string) (string, error) {
	email := strings.TrimSpace(raw)
	if email == "" || len(email) > MaxEmailLength {
		return "", &ValidationError{Field: "email", Message: "a valid email address is required"}
	}
	addr, err := mail.ParseAddress(email)
	// Reject display-name forms like "Bob <bob@x.com>" and addresses without a dotted domain.
	if err != nil || addr.Address != email || !strings.Contains(email[strings.LastIndex(email, "@")+1:], ".") {
		return "", &ValidationError{Field: "email", Message: "a valid email address is required"}
	}
	return email, nil
}

// validatePassword follows NIST SP 800-63B: length matters, composition rules
// do not. Checking against breached-password lists is a later addition.
func validatePassword(p string) error {
	n := utf8.RuneCountInString(p)
	if !utf8.ValidString(p) || n < MinPasswordLength || n > MaxPasswordLength {
		return &ValidationError{
			Field:   "password",
			Message: fmt.Sprintf("password must be %d to %d characters", MinPasswordLength, MaxPasswordLength),
		}
	}
	return nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
