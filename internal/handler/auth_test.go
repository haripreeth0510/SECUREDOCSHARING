package handler_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/securedocsharing/app/internal/auth"
	"github.com/securedocsharing/app/internal/handler"
	"github.com/securedocsharing/app/internal/ratelimit"
	"github.com/securedocsharing/app/internal/repository"
	"github.com/securedocsharing/app/internal/service"
)

// testApp is the full auth stack on a rolled-back DB transaction and a
// per-test Redis namespace.
type testApp struct {
	t   *testing.T
	h   http.Handler
	tx  pgx.Tx
	ip  string
	svc *service.AuthService
	rdb *redis.Client
}

func newTestApp(t *testing.T, limits handler.AuthLimits) *testApp {
	t.Helper()
	return newTestAppWithAccountLimit(t, limits, 100)
}

func newTestAppWithAccountLimit(t *testing.T, limits handler.AuthLimits, accountLimit int) *testApp {
	t.Helper()
	ctx := context.Background()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / DATABASE_URL not set")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("database not reachable: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })

	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr, Password: os.Getenv("REDIS_PASSWORD")})
	t.Cleanup(func() { _ = rdb.Close() })
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("redis not reachable: %v", err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	prefix := "test:" + hex.EncodeToString(b) + ":"

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	issuer, err := auth.NewTokenIssuer([]byte("0123456789abcdef0123456789abcdef"), "sds", "sds-api", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	limiter := ratelimit.NewRedis(rdb, prefix)
	svc, err := service.NewAuthService(ctx,
		repository.NewUserRepo(tx), repository.NewRefreshTokenRepo(tx), repository.NewAuditRepo(tx),
		auth.NewPasswordHasher(auth.Argon2Params{Memory: 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}, 4),
		issuer, auth.NewRedisDenylist(rdb, prefix), limiter,
		service.AuthConfig{RefreshTTL: time.Hour, TermsVersion: "1.0", LoginLimit: service.LoginLimit{Limit: accountLimit, Window: time.Minute}},
		log)
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	handler.NewAuthHandler(svc, limiter, limits, true, log).RegisterRoutes(mux)
	return &testApp{t: t, h: handler.Recoverer(log, handler.NoStore(mux)), tx: tx, ip: "192.0.2.10", svc: svc, rdb: rdb}
}

var generous = handler.AuthLimits{
	Register: handler.RouteLimit{Limit: 100, Window: time.Minute},
	Login:    handler.RouteLimit{Limit: 100, Window: time.Minute},
	Refresh:  handler.RouteLimit{Limit: 100, Window: time.Minute},
}

type reqOpt func(*http.Request)

func withBearer(tok string) reqOpt {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}
func withCookie(c *http.Cookie) reqOpt { return func(r *http.Request) { r.AddCookie(c) } }

func (a *testApp) do(method, path, body string, opts ...reqOpt) *httptest.ResponseRecorder {
	a.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, path, rd)
	r.RemoteAddr = a.ip + ":40000"
	r.Header.Set("User-Agent", "auth-test")
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	a.h.ServeHTTP(w, r)
	return w
}

type apiResp struct {
	Success bool `json:"success"`
	Data    struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
		Email       string `json:"email"`
		ID          string `json:"id"`
		Message     string `json:"message"`
	} `json:"data"`
	Error *struct {
		Code  string `json:"code"`
		Field string `json:"field"`
	} `json:"error"`
}

func decode(t *testing.T, w *httptest.ResponseRecorder) apiResp {
	t.Helper()
	var r apiResp
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
			t.Fatalf("decode %q: %v", w.Body.String(), err)
		}
	}
	return r
}

func expect(t *testing.T, w *httptest.ResponseRecorder, status int, code string) apiResp {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d, want %d; body %s", w.Code, status, w.Body.String())
	}
	r := decode(t, w)
	if code != "" && (r.Error == nil || r.Error.Code != code) {
		t.Fatalf("want error code %q, got body %s", code, w.Body.String())
	}
	return r
}

func refreshCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == "refresh_token" {
			return c
		}
	}
	t.Fatalf("no refresh_token cookie in %v", w.Header()["Set-Cookie"])
	return nil
}

const (
	email    = "auth-flow@example.com"
	password = "a-long-enough-passphrase"
)

func (a *testApp) register() {
	a.t.Helper()
	expect(a.t, a.do("POST", "/api/v1/auth/register",
		`{"email":"`+email+`","password":"`+password+`","accept_terms":true}`), http.StatusAccepted, "")
}

func (a *testApp) login() (string, *http.Cookie) {
	a.t.Helper()
	w := a.do("POST", "/api/v1/auth/login", `{"email":"`+email+`","password":"`+password+`"}`)
	r := expect(a.t, w, http.StatusOK, "")
	return r.Data.AccessToken, refreshCookie(a.t, w)
}

func (a *testApp) auditCount(action, result string) int {
	a.t.Helper()
	var n int
	if err := a.tx.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_logs WHERE action = $1 AND result = $2 AND user_agent = 'auth-test'`,
		action, result).Scan(&n); err != nil {
		a.t.Fatal(err)
	}
	return n
}

func TestRegisterValidation(t *testing.T) {
	a := newTestApp(t, generous)
	cases := []struct {
		name, body, field string
	}{
		{"bad email", `{"email":"nope","password":"` + password + `","accept_terms":true}`, "email"},
		{"display name email", `{"email":"Bob <b@example.com>","password":"` + password + `","accept_terms":true}`, "email"},
		{"short password", `{"email":"x@example.com","password":"short","accept_terms":true}`, "password"},
		{"terms not accepted", `{"email":"x@example.com","password":"` + password + `","accept_terms":false}`, "accept_terms"},
		{"unknown field", `{"email":"x@example.com","password":"` + password + `","accept_terms":true,"admin":true}`, ""},
		{"two objects", `{"email":"x@example.com"}{}`, ""},
		{"not json", `email=x`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := expect(t, a.do("POST", "/api/v1/auth/register", c.body), http.StatusBadRequest, handler.CodeInvalidRequest)
			if r.Error.Field != c.field {
				t.Fatalf("field %q, want %q", r.Error.Field, c.field)
			}
		})
	}

	// Wrong content type.
	r := httptest.NewRequest("POST", "/api/v1/auth/register", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	a.h.ServeHTTP(w, r)
	expect(t, w, http.StatusBadRequest, handler.CodeInvalidRequest)
}

func TestRegisterDoesNotRevealExistingAccounts(t *testing.T) {
	a := newTestApp(t, generous)
	first := a.do("POST", "/api/v1/auth/register", `{"email":"`+email+`","password":"`+password+`","accept_terms":true}`)
	dup := a.do("POST", "/api/v1/auth/register", `{"email":"`+strings.ToUpper(email)+`","password":"other-passphrase-123","accept_terms":true}`)
	if first.Code != http.StatusAccepted || dup.Code != first.Code || dup.Body.String() != first.Body.String() {
		t.Fatalf("responses differ:\n%d %s\n%d %s", first.Code, first.Body, dup.Code, dup.Body)
	}
	if a.auditCount(service.ActionRegister, "success") != 1 || a.auditCount(service.ActionRegisterDuplicate, "failure") != 1 {
		t.Fatal("register events not audited")
	}
	// The duplicate must not have changed the original password.
	a.login()
}

func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	a := newTestApp(t, generous)
	a.register()

	wrong := a.do("POST", "/api/v1/auth/login", `{"email":"`+email+`","password":"wrong-password-123"}`)
	unknown := a.do("POST", "/api/v1/auth/login", `{"email":"nobody@example.com","password":"wrong-password-123"}`)
	expect(t, wrong, http.StatusUnauthorized, handler.CodeInvalidCredentials)
	if unknown.Code != wrong.Code || unknown.Body.String() != wrong.Body.String() {
		t.Fatalf("unknown-user response differs: %s vs %s", unknown.Body, wrong.Body)
	}
	if len(wrong.Result().Cookies()) != 0 {
		t.Fatal("failed login must not set cookies")
	}
	if a.auditCount(service.ActionLogin, "failure") != 2 {
		t.Fatal("failed logins not audited")
	}
}

func TestFullSessionLifecycle(t *testing.T) {
	a := newTestApp(t, generous)
	a.register()

	// Login: access token in body, refresh token in a locked-down cookie.
	w := a.do("POST", "/api/v1/auth/login", `{"email":"`+email+`","password":"`+password+`"}`)
	r := expect(t, w, http.StatusOK, "")
	if r.Data.TokenType != "Bearer" || r.Data.ExpiresIn < 890 || r.Data.ExpiresIn > 900 {
		t.Fatalf("token response: %+v", r.Data)
	}
	if strings.Contains(w.Body.String(), "refresh") {
		t.Fatal("refresh token must not appear in the response body")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("token responses must not be cacheable")
	}
	c := refreshCookie(t, w)
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/api/v1/auth" {
		t.Fatalf("refresh cookie not locked down: %+v", c)
	}
	access := r.Data.AccessToken

	// /me
	me := expect(t, a.do("GET", "/api/v1/auth/me", "", withBearer(access)), http.StatusOK, "")
	if me.Data.Email != email || me.Data.ID == "" {
		t.Fatalf("me: %+v", me.Data)
	}
	if strings.Contains(a.do("GET", "/api/v1/auth/me", "", withBearer(access)).Body.String(), "argon2") {
		t.Fatal("password hash leaked")
	}
	expect(t, a.do("GET", "/api/v1/auth/me", ""), http.StatusUnauthorized, handler.CodeUnauthorized)
	expect(t, a.do("GET", "/api/v1/auth/me", "", withBearer("garbage")), http.StatusUnauthorized, handler.CodeUnauthorized)

	// Refresh rotates the cookie and issues a new access token.
	w = a.do("POST", "/api/v1/auth/refresh", "", withCookie(c))
	r = expect(t, w, http.StatusOK, "")
	c2 := refreshCookie(t, w)
	if c2.Value == c.Value || r.Data.AccessToken == "" {
		t.Fatal("refresh must rotate the token")
	}

	// Replaying the old refresh token is theft: it fails AND kills the new one.
	expect(t, a.do("POST", "/api/v1/auth/refresh", "", withCookie(c)), http.StatusUnauthorized, handler.CodeUnauthorized)
	expect(t, a.do("POST", "/api/v1/auth/refresh", "", withCookie(c2)), http.StatusUnauthorized, handler.CodeUnauthorized)
	if a.auditCount(service.ActionTokenReuse, "failure") != 1 {
		t.Fatal("token reuse not audited")
	}

	// Logout revokes the refresh token and denies the access token at once.
	access, c3 := a.login()
	w = a.do("POST", "/api/v1/auth/logout", "", withBearer(access), withCookie(c3))
	if w.Code != http.StatusNoContent {
		t.Fatalf("logout: %d %s", w.Code, w.Body)
	}
	if cleared := refreshCookie(t, w); cleared.MaxAge >= 0 || cleared.Value != "" {
		t.Fatalf("logout must clear the cookie: %+v", cleared)
	}
	expect(t, a.do("GET", "/api/v1/auth/me", "", withBearer(access)), http.StatusUnauthorized, handler.CodeUnauthorized)
	expect(t, a.do("POST", "/api/v1/auth/refresh", "", withCookie(c3)), http.StatusUnauthorized, handler.CodeUnauthorized)
	if a.auditCount(service.ActionLogout, "success") != 1 {
		t.Fatal("logout not audited")
	}

	// Logout without any credentials is a harmless no-op.
	if w := a.do("POST", "/api/v1/auth/logout", ""); w.Code != http.StatusNoContent {
		t.Fatalf("anonymous logout: %d", w.Code)
	}
}

func TestRefreshWithoutCookie(t *testing.T) {
	a := newTestApp(t, generous)
	expect(t, a.do("POST", "/api/v1/auth/refresh", ""), http.StatusUnauthorized, handler.CodeUnauthorized)
}

func TestLoginRateLimitedPerIP(t *testing.T) {
	limits := generous
	limits.Login = handler.RouteLimit{Limit: 5, Window: time.Minute}
	a := newTestApp(t, limits)

	for i := 0; i < 5; i++ {
		expect(t, a.do("POST", "/api/v1/auth/login", `{"email":"x@example.com","password":"wrong-password-123"}`),
			http.StatusUnauthorized, handler.CodeInvalidCredentials)
	}
	w := a.do("POST", "/api/v1/auth/login", `{"email":"x@example.com","password":"wrong-password-123"}`)
	expect(t, w, http.StatusTooManyRequests, handler.CodeRateLimited)
	if ra := w.Header().Get("Retry-After"); ra == "" || ra == "0" {
		t.Fatalf("missing Retry-After, got %q", ra)
	}

	// A different client IP is not affected.
	a.ip = "192.0.2.99"
	expect(t, a.do("POST", "/api/v1/auth/login", `{"email":"x@example.com","password":"wrong-password-123"}`),
		http.StatusUnauthorized, handler.CodeInvalidCredentials)
}

func TestLoginRateLimitedPerAccountAcrossIPs(t *testing.T) {
	// IP limits stay generous; only the per-account limit (3) applies.
	a := newTestAppWithAccountLimit(t, generous, 3)
	for _, ip := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.3"} {
		a.ip = ip
		expect(t, a.do("POST", "/api/v1/auth/login", `{"email":"victim@example.com","password":"wrong-password-123"}`),
			http.StatusUnauthorized, handler.CodeInvalidCredentials)
	}
	// A fresh IP is still blocked for this account, whatever the email's case.
	a.ip = "198.51.100.9"
	expect(t, a.do("POST", "/api/v1/auth/login", `{"email":"VICTIM@example.com","password":"wrong-password-123"}`),
		http.StatusTooManyRequests, handler.CodeRateLimited)
}

func TestFailsClosedWhenRedisIsDown(t *testing.T) {
	a := newTestApp(t, generous)
	a.register()
	access, _ := a.login()

	_ = a.rdb.Close() // simulate a Redis outage

	// Rate limits and the logout denylist live in Redis. Without it the API
	// must refuse requests, not silently skip brute-force and revocation checks.
	expect(t, a.do("POST", "/api/v1/auth/login", `{"email":"`+email+`","password":"`+password+`"}`),
		http.StatusServiceUnavailable, handler.CodeUnavailable)
	expect(t, a.do("GET", "/api/v1/auth/me", "", withBearer(access)),
		http.StatusServiceUnavailable, handler.CodeUnavailable)
}

func TestLoginUpgradesOutdatedPasswordHash(t *testing.T) {
	a := newTestApp(t, generous)
	ctx := context.Background()

	// A hash made with older (different) parameters than the service uses.
	old := auth.NewPasswordHasher(auth.Argon2Params{Memory: 1024, Iterations: 2, Parallelism: 1, SaltLength: 16, KeyLength: 32}, 1)
	h, err := old.Hash(ctx, password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.NewUserRepo(a.tx).CreateWithConsent(ctx, email, h, "1.0", time.Now()); err != nil {
		t.Fatal(err)
	}

	a.login()

	var stored string
	if err := a.tx.QueryRow(ctx, `SELECT password_hash FROM users WHERE email = $1`, email).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored, "m=1024,t=1,p=1") {
		t.Fatalf("hash not upgraded to current params: %s", stored)
	}
	a.login() // and the upgraded hash still works
}

func TestRecovererHidesPanics(t *testing.T) {
	h := handler.Recoverer(slog.New(slog.NewTextHandler(io.Discard, nil)),
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("db password is hunter2") }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	expect(t, w, http.StatusInternalServerError, handler.CodeInternal)
	if strings.Contains(w.Body.String(), "hunter2") {
		t.Fatal("panic value leaked to the client")
	}
}

func TestLoginWithMalformedStoredHashIsAFailedLogin(t *testing.T) {
	a := newTestApp(t, generous)
	if _, err := repository.NewUserRepo(a.tx).CreateWithConsent(context.Background(),
		email, "DUMMY-NOT-A-REAL-ARGON2ID-HASH", "1.0", time.Now()); err != nil {
		t.Fatal(err)
	}
	expect(t, a.do("POST", "/api/v1/auth/login", `{"email":"`+email+`","password":"`+password+`"}`),
		http.StatusUnauthorized, handler.CodeInvalidCredentials)
}
