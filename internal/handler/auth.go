package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/securedocsharing/app/internal/auth"
	"github.com/securedocsharing/app/internal/ratelimit"
	"github.com/securedocsharing/app/internal/service"
)

const (
	refreshCookieName = "refresh_token"
	refreshCookiePath = "/api/v1/auth" // the cookie is only ever sent to auth endpoints
)

// RouteLimit is one per-IP rate limit.
type RouteLimit struct {
	Limit  int
	Window time.Duration
}

// AuthLimits are the per-IP limits for the public auth endpoints.
type AuthLimits struct {
	Register RouteLimit
	Login    RouteLimit
	Refresh  RouteLimit
}

type AuthHandler struct {
	svc          *service.AuthService
	limiter      ratelimit.Limiter
	limits       AuthLimits
	secureCookie bool
	log          *slog.Logger
}

// NewAuthHandler builds the auth routes. secureCookie must be true everywhere
// except local HTTP development.
func NewAuthHandler(svc *service.AuthService, limiter ratelimit.Limiter, limits AuthLimits, secureCookie bool, log *slog.Logger) *AuthHandler {
	return &AuthHandler{svc: svc, limiter: limiter, limits: limits, secureCookie: secureCookie, log: log}
}

func (h *AuthHandler) RegisterRoutes(mux *http.ServeMux) {
	rl := func(name string, l RouteLimit, f http.HandlerFunc) http.HandlerFunc {
		return RateLimit(h.limiter, h.log, name, l.Limit, l.Window, f)
	}
	mux.HandleFunc("POST /api/v1/auth/register", rl("register", h.limits.Register, h.Register))
	mux.HandleFunc("POST /api/v1/auth/login", rl("login", h.limits.Login, h.Login))
	mux.HandleFunc("POST /api/v1/auth/refresh", rl("refresh", h.limits.Refresh, h.Refresh))
	mux.HandleFunc("POST /api/v1/auth/logout", rl("refresh", h.limits.Refresh, h.Logout))
	mux.HandleFunc("GET /api/v1/auth/me", RequireAuth(h.svc, h.Me))
}

type registerRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	AcceptTerms bool   `json:"accept_terms"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

type userResponse struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

// Register always answers 202 with the same body for valid input, whether or
// not the email was already registered, so the endpoint cannot be used to
// discover accounts.
func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, err.Error(), "")
		return
	}
	if err := h.svc.Register(r.Context(), meta(r), req.Email, req.Password, req.AcceptTerms); err != nil {
		h.fail(w, r, err)
		return
	}
	writeData(w, http.StatusAccepted, map[string]string{
		"message": "Registration received. If the email address can be used, you can now sign in.",
	})
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, err.Error(), "")
		return
	}
	pair, err := h.svc.Login(r.Context(), meta(r), req.Email, req.Password)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeTokens(w, pair)
}

func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	pair, err := h.svc.Refresh(r.Context(), meta(r), refreshCookie(r))
	if err != nil {
		if errors.Is(err, service.ErrInvalidToken) {
			h.clearRefreshCookie(w)
		}
		h.fail(w, r, err)
		return
	}
	h.writeTokens(w, pair)
}

// Logout needs no valid access token, so a user whose access token has
// expired can still end the session. A bearer token, if valid, is denied too.
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	claims := h.optionalClaims(r)
	if err := h.svc.Logout(r.Context(), meta(r), claims, refreshCookie(r)); err != nil {
		h.fail(w, r, err)
		return
	}
	h.clearRefreshCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	claims, _ := ClaimsFrom(r.Context())
	u, err := h.svc.Me(r.Context(), claims.UserID())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeData(w, http.StatusOK, userResponse{ID: u.ID, Email: u.Email, CreatedAt: u.CreatedAt})
}

func (h *AuthHandler) optionalClaims(r *http.Request) *auth.AccessClaims {
	token, ok := bearerToken(r)
	if !ok {
		return nil
	}
	c, err := h.svc.Authenticate(r.Context(), token)
	if err != nil {
		return nil
	}
	return c
}

func (h *AuthHandler) writeTokens(w http.ResponseWriter, p *service.TokenPair) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookieName,
		Value:    p.RefreshToken,
		Path:     refreshCookiePath,
		Expires:  p.RefreshExpiresAt,
		MaxAge:   int(time.Until(p.RefreshExpiresAt).Seconds()),
		HttpOnly: true, // not readable by page scripts, so XSS cannot steal it
		Secure:   h.secureCookie,
		SameSite: http.SameSiteStrictMode, // not sent on cross-site requests (CSRF)
	})
	writeData(w, http.StatusOK, tokenResponse{
		AccessToken: p.AccessToken,
		TokenType:   "Bearer",
		ExpiresIn:   int(time.Until(p.AccessExpiresAt).Seconds()),
	})
}

func (h *AuthHandler) clearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: refreshCookieName, Value: "", Path: refreshCookiePath, MaxAge: -1,
		HttpOnly: true, Secure: h.secureCookie, SameSite: http.SameSiteStrictMode,
	})
}

// fail maps service errors to responses. Unknown errors are logged and
// returned as a generic 500 so internal details never reach the client.
func (h *AuthHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var verr *service.ValidationError
	var rlerr *service.RateLimitError
	switch {
	case errors.As(err, &verr):
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, verr.Message, verr.Field)
	case errors.As(err, &rlerr):
		writeRateLimited(w, rlerr.RetryAfter)
	case errors.Is(err, service.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, CodeInvalidCredentials, "invalid email or password", "")
	case errors.Is(err, service.ErrInvalidToken):
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "invalid or expired token", "")
	case errors.Is(err, service.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, CodeUnavailable, "service temporarily unavailable", "")
	default:
		h.log.ErrorContext(r.Context(), "request failed", "path", r.URL.Path, "error", err)
		writeError(w, http.StatusInternalServerError, CodeInternal, "internal server error", "")
	}
}

func refreshCookie(r *http.Request) string {
	c, err := r.Cookie(refreshCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

func meta(r *http.Request) service.RequestMeta {
	// Postgres rejects invalid UTF-8, and the header is client-controlled.
	ua := r.UserAgent()
	if len(ua) > 512 {
		ua = ua[:512]
	}
	ua = strings.ToValidUTF8(ua, "")
	return service.RequestMeta{IP: clientIP(r), UserAgent: ua}
}
