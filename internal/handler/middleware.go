package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/securedocsharing/app/internal/auth"
	"github.com/securedocsharing/app/internal/ratelimit"
	"github.com/securedocsharing/app/internal/service"
)

type ctxKey int

const claimsKey ctxKey = iota

// ClaimsFrom returns the access-token claims set by RequireAuth.
func ClaimsFrom(ctx context.Context) (*auth.AccessClaims, bool) {
	c, ok := ctx.Value(claimsKey).(*auth.AccessClaims)
	return c, ok
}

// Authenticator is the part of the auth service the middleware needs.
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (*auth.AccessClaims, error)
}

// RequireAuth rejects requests without a valid, non-revoked bearer token.
func RequireAuth(a Authenticator, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer`)
			writeError(w, http.StatusUnauthorized, CodeUnauthorized, "authentication required", "")
			return
		}
		claims, err := a.Authenticate(r.Context(), token)
		if errors.Is(err, service.ErrUnavailable) {
			writeError(w, http.StatusServiceUnavailable, CodeUnavailable, "service temporarily unavailable", "")
			return
		}
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			writeError(w, http.StatusUnauthorized, CodeUnauthorized, "invalid or expired token", "")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), claimsKey, claims)))
	}
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	scheme, token, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

// RateLimit applies a per-client-IP sliding-window limit to one route. If the
// limiter is unreachable the request is refused (fail closed): these limits
// protect against brute force, so skipping them silently is not acceptable.
func RateLimit(l ratelimit.Limiter, log *slog.Logger, name string, limit int, window time.Duration, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d, err := l.Allow(r.Context(), name+":ip:"+clientIP(r), limit, window)
		if err != nil {
			log.ErrorContext(r.Context(), "rate limiter unavailable", "route", name, "error", err)
			writeError(w, http.StatusServiceUnavailable, CodeUnavailable, "service temporarily unavailable", "")
			return
		}
		if !d.Allowed {
			writeRateLimited(w, d.RetryAfter)
			return
		}
		next(w, r)
	}
}

func writeRateLimited(w http.ResponseWriter, retryAfter time.Duration) {
	secs := int((retryAfter + time.Second - 1) / time.Second)
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, http.StatusTooManyRequests, CodeRateLimited, "too many requests, try again later", "")
}

// NoStore stops browsers and proxies caching responses that carry tokens or
// personal data.
func NoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		next.ServeHTTP(w, r)
	})
}

// Recoverer turns a panic into a 500 without leaking the panic value.
func Recoverer(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				log.ErrorContext(r.Context(), "panic serving request", "panic", v, "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, CodeInternal, "internal server error", "")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
