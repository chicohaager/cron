package auth

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// signES256 builds a minimal ES256 JWT signed with key and carrying iss.
func signES256(t *testing.T, key *ecdsa.PrivateKey, exp int64, iss string) string {
	t.Helper()
	hdr := b64.EncodeToString([]byte(`{"alg":"ES256","typ":"JWT"}`))
	payload := b64.EncodeToString([]byte(`{"iss":"` + iss + `","exp":` + strconv.FormatInt(exp, 10) + `}`))
	signingInput := hdr + "." + payload
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signingInput + "." + b64.EncodeToString(sig)
}

func coords(pub *ecdsa.PublicKey) (x, y []byte) {
	x = make([]byte, 32)
	y = make([]byte, 32)
	pub.X.FillBytes(x)
	pub.Y.FillBytes(y)
	return x, y
}

// jwksServer serves a JWKS containing pub as its only EC/P-256 key.
func jwksServer(t *testing.T, pub *ecdsa.PublicKey) *httptest.Server {
	t.Helper()
	x, y := coords(pub)
	body, _ := json.Marshal(map[string]interface{}{
		"keys": []map[string]string{{"kty": "EC", "crv": "P-256",
			"x": b64.EncodeToString(x), "y": b64.EncodeToString(y)}},
	})
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
}

func newKeyAndServer(t *testing.T) (*ecdsa.PrivateKey, *Verifier) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	srv := jwksServer(t, &key.PublicKey)
	t.Cleanup(srv.Close)
	return key, NewVerifier(StaticURL(srv.URL))
}

func TestVerifyValidToken(t *testing.T) {
	key, v := newKeyAndServer(t)
	if err := v.Verify(signES256(t, key, time.Now().Add(time.Hour).Unix(), "zimaos")); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
}

func TestVerifyExpiredToken(t *testing.T) {
	key, v := newKeyAndServer(t)
	err := v.Verify(signES256(t, key, time.Now().Add(-time.Minute).Unix(), "zimaos"))
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired token accepted: %v", err)
	}
}

// Both platform issuers are sessions: "casaos" on ≤1.7.0, "zimaos" on ≥1.7.1.
func TestVerifyAcceptsBothPlatformIssuers(t *testing.T) {
	key, v := newKeyAndServer(t)
	for _, iss := range []string{"casaos", "zimaos"} {
		if err := v.Verify(signES256(t, key, time.Now().Add(time.Hour).Unix(), iss)); err != nil {
			t.Errorf("issuer %q rejected: %v", iss, err)
		}
	}
}

// The refresh token is signed with the same key but must not open the API.
func TestVerifyRejectsNonSessionIssuer(t *testing.T) {
	key, v := newKeyAndServer(t)
	err := v.Verify(signES256(t, key, time.Now().Add(time.Hour).Unix(), "refresh"))
	if err == nil || !strings.Contains(err.Error(), `issuer "refresh"`) {
		t.Fatalf("refresh token accepted: %v", err)
	}
}

func TestVerifyForeignSignature(t *testing.T) {
	_, v := newKeyAndServer(t)
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	err := v.Verify(signES256(t, other, time.Now().Add(time.Hour).Unix(), "zimaos"))
	if err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("foreign signature accepted: %v", err)
	}
}

func TestVerifyMalformed(t *testing.T) {
	_, v := newKeyAndServer(t)
	for _, tok := range []string{"", "abc", "a.b", "a.b.c"} {
		if v.Verify(tok) == nil {
			t.Errorf("malformed token %q accepted", tok)
		}
	}
}

func TestMiddlewareRejectsMissingTokenWithJSON(t *testing.T) {
	_, v := newKeyAndServer(t)
	h := v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/cron/tasks", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["code"] != "auth_required" {
		t.Fatalf("body %q, want JSON with code auth_required", rec.Body.String())
	}
}

func TestDisabledVerifierPassesThrough(t *testing.T) {
	h := Disabled().Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/cron/tasks", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d, want 200", rec.Code)
	}
}

// A refused request must leave a line in the journal naming the reason and
// the client — but never the token.
func TestMiddlewareLogsRejection(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	key, v := newKeyAndServer(t)
	h := v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	req := httptest.NewRequest(http.MethodGet, "/cron/tasks", nil)
	req.Header.Set("Authorization", "Bearer "+signES256(t, key, time.Now().Add(time.Hour).Unix(), "refresh"))
	req.Header.Set("X-Forwarded-For", "192.0.2.7")
	h.ServeHTTP(httptest.NewRecorder(), req)

	line := buf.String()
	for _, want := range []string{"session rejected", `issuer "refresh"`, "/cron/tasks", "192.0.2.7"} {
		if !strings.Contains(line, want) {
			t.Errorf("log line does not mention %q:\n%s", want, line)
		}
	}
	if strings.Contains(line, "eyJ") {
		t.Errorf("log line appears to contain the token:\n%s", line)
	}
}

func TestRejectLogRateLimits(t *testing.T) {
	var l rejectLog
	now := time.Now()
	if ok, n := l.admit(now); !ok || n != 0 {
		t.Fatalf("first: admit=%v suppressed=%d", ok, n)
	}
	for i := 0; i < 5; i++ {
		if ok, _ := l.admit(now.Add(time.Second)); ok {
			t.Fatalf("rejection %d within the interval was logged", i)
		}
	}
	if ok, n := l.admit(now.Add(rejectLogInterval)); !ok || n != 5 {
		t.Fatalf("after interval: admit=%v suppressed=%d, want true/5", ok, n)
	}
}

func TestP256PublicKeyPadsShortCoordinatesAndRejectsOffCurve(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	x, y := coords(&key.PublicKey)
	full, err := p256PublicKey(x, y)
	if err != nil || !full.Equal(&key.PublicKey) {
		t.Fatalf("full-length coordinates: %v", err)
	}
	trim := func(b []byte) []byte {
		for len(b) > 1 && b[0] == 0 {
			b = b[1:]
		}
		return b
	}
	short, err := p256PublicKey(trim(x), trim(y))
	if err != nil || !short.Equal(&key.PublicKey) {
		t.Fatalf("stripped coordinates: %v", err)
	}
	bad := append([]byte(nil), y...)
	bad[31] ^= 1
	if _, err := p256PublicKey(x, bad); err == nil {
		t.Fatal("off-curve point accepted")
	}
	if _, err := p256PublicKey(append(x, 0), y); err == nil {
		t.Fatal("over-long coordinate accepted")
	}
}

// JWKSResolver must find the route through the gateway table and refuse a
// target that does not point at loopback.
func TestJWKSResolver(t *testing.T) {
	serve := func(target string) (string, func()) {
		gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/gateway/routes" {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]string{
				{"path": "/cron", "target": "http://127.0.0.1:41617"},
				{"path": jwksRoute, "target": target},
			})
		}))
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "management.url"), []byte(gw.URL+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		return dir, gw.Close
	}

	dir, done := serve("http://127.0.0.1:37815")
	defer done()
	got, err := JWKSResolver(dir)(context.Background())
	if err != nil || got != "http://127.0.0.1:37815"+jwksRoute {
		t.Fatalf("resolver = %q, %v", got, err)
	}

	dir2, done2 := serve("http://203.0.113.9:37815")
	defer done2()
	if _, err := JWKSResolver(dir2)(context.Background()); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("off-host target accepted: %v", err)
	}

	if _, err := JWKSResolver(t.TempDir())(context.Background()); err == nil {
		t.Fatal("missing management.url must fail")
	}
}
