package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var fastParams = Argon2Params{Memory: 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}

func TestPasswordHashAndVerify(t *testing.T) {
	ctx := context.Background()
	h := NewPasswordHasher(fastParams, 2)

	enc, err := h.Hash(ctx, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=1024,t=1,p=1$") {
		t.Fatalf("unexpected encoding: %s", enc)
	}

	if ok, err := h.Verify(ctx, "correct horse battery staple", enc); err != nil || !ok {
		t.Fatalf("correct password: ok=%v err=%v", ok, err)
	}
	if ok, _ := h.Verify(ctx, "wrong password", enc); ok {
		t.Fatal("wrong password verified")
	}

	// Same password, different salt, different hash.
	enc2, _ := h.Hash(ctx, "correct horse battery staple")
	if enc == enc2 {
		t.Fatal("hashes must be salted")
	}
}

func TestPasswordVerifyUsesStoredParams(t *testing.T) {
	ctx := context.Background()
	old := NewPasswordHasher(fastParams, 1)
	enc, _ := old.Hash(ctx, "a long enough password")

	stronger := fastParams
	stronger.Iterations = 2
	cur := NewPasswordHasher(stronger, 1)

	if ok, err := cur.Verify(ctx, "a long enough password", enc); err != nil || !ok {
		t.Fatalf("old-params hash must still verify: ok=%v err=%v", ok, err)
	}
	if !cur.NeedsRehash(enc) {
		t.Fatal("NeedsRehash should be true after a parameter change")
	}
	if old.NeedsRehash(enc) {
		t.Fatal("NeedsRehash should be false with matching params")
	}
}

func TestPasswordRejectsBadHashes(t *testing.T) {
	h := NewPasswordHasher(fastParams, 1)
	for _, bad := range []string{
		"",
		"plaintext",
		"$argon2i$v=19$m=1024,t=1,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=18$m=1024,t=1,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=99999999,t=1,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA", // memory bomb
		"$argon2id$v=19$m=1024,t=1,p=1$!!!$aGFzaGhhc2hoYXNoaGFzaA",
	} {
		if _, err := h.Verify(context.Background(), "x", bad); err != ErrInvalidHash {
			t.Errorf("%q: want ErrInvalidHash, got %v", bad, err)
		}
	}
}

func TestPasswordHashHonoursContext(t *testing.T) {
	h := NewPasswordHasher(fastParams, 1)
	h.sem <- struct{}{} // occupy the only slot
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := h.Hash(ctx, "a long enough password"); err == nil {
		t.Fatal("expected context error while the hasher is saturated")
	}
}

func newIssuer(t *testing.T, secret string) *TokenIssuer {
	t.Helper()
	ti, err := NewTokenIssuer([]byte(secret), "sds", "sds-api", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return ti
}

const secretA = "0123456789abcdef0123456789abcdef"

func TestTokenRoundTrip(t *testing.T) {
	ti := newIssuer(t, secretA)
	tok, claims, err := ti.Issue("user-1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ti.Parse(tok)
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID() != "user-1" || got.ID != claims.ID || got.ID == "" {
		t.Fatalf("claims mismatch: %+v", got)
	}
	if d := time.Until(got.ExpiresAt.Time); d < 14*time.Minute || d > 15*time.Minute {
		t.Fatalf("unexpected expiry in %v", d)
	}
}

func TestTokenRejections(t *testing.T) {
	ti := newIssuer(t, secretA)
	good, _, _ := ti.Issue("user-1")

	other := newIssuer(t, "fedcba9876543210fedcba9876543210")
	forged, _, _ := other.Issue("user-1")

	expired := newIssuer(t, secretA)
	expired.now = func() time.Time { return time.Now().Add(-time.Hour) }
	old, _, _ := expired.Issue("user-1")

	wrongAud, _ := NewTokenIssuer([]byte(secretA), "sds", "other-api", time.Minute)
	aud, _, _ := wrongAud.Issue("user-1")

	// alg=none must never be accepted.
	none, _ := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"sub": "user-1", "iss": "sds", "aud": "sds-api", "jti": "x",
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
	}).SignedString(jwt.UnsafeAllowNoneSignatureType)

	tampered := good[:len(good)-2] + "xx"

	for name, tok := range map[string]string{
		"wrong secret": forged, "expired": old, "wrong audience": aud,
		"alg none": none, "tampered": tampered, "garbage": "not.a.jwt", "empty": "",
	} {
		if _, err := ti.Parse(tok); err != ErrInvalidToken {
			t.Errorf("%s: want ErrInvalidToken, got %v", name, err)
		}
	}
}

func TestTokenIssuerRejectsShortSecret(t *testing.T) {
	if _, err := NewTokenIssuer([]byte("short"), "i", "a", time.Minute); err == nil {
		t.Fatal("short secret accepted")
	}
}

func TestRefreshTokens(t *testing.T) {
	a, _ := NewRefreshToken()
	b, _ := NewRefreshToken()
	if a == b || len(a) != 43 { // 32 bytes, base64url without padding
		t.Fatalf("unexpected refresh tokens %q %q", a, b)
	}
	if HashToken(a) == a || HashToken(a) != HashToken(a) || len(HashToken(a)) != 64 {
		t.Fatal("HashToken must be a deterministic hex SHA-256")
	}
}
