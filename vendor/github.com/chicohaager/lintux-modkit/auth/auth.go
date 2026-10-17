// Package auth verifies ZimaOS session tokens so the task API — which runs
// arbitrary commands as root — is not reachable unauthenticated. ZimaOS
// issues ES256 JWTs (the web UI keeps one in localStorage.access_token); the
// gateway proxies module routes without checking them, so every module has
// to verify the token itself. This package checks the signature against the
// platform JWKS, the validity window and the issuer.
//
// It originated in ZFW (github.com/chicohaager/zfw) and moved to cron; the
// measurements of the ZimaOS token format are recorded in the comments.
package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// jwksTTL is how long a fetched key set is reused before a refresh.
	jwksTTL = 10 * time.Minute
	// jwksMaxStale caps how long a cached key set keeps being served once
	// refreshes start failing; past this the verifier fails closed rather
	// than trusting keys that may since have been rotated.
	jwksMaxStale = 1 * time.Hour
	// clockSkew is the tolerance applied when checking nbf.
	clockSkew = 60 * time.Second
	// jwksRetryFloor is the least age a cached key set must have before a
	// token that matches none of its keys triggers an early refresh. The
	// user service issues a new key when it restarts (measured on ZimaOS
	// 1.7.1 after a system restore: fresh logins failed for the rest of
	// the TTL); a floor keeps a stream of garbage tokens from hammering it.
	jwksRetryFloor = 15 * time.Second
	// jwksRoute is the gateway route under which the user-service publishes
	// its keys. Its target port is assigned by the system and changes across
	// restarts, so it is looked up, never pinned.
	jwksRoute = "/.well-known/jwks.json"
)

// sessionIssuers are the `iss` claims a ZimaOS *access* token may carry.
// The user-service mints other tokens with the same signing key — notably
// the long-lived refresh token (iss "refresh") — which must not authorise
// the API. ZimaOS renamed the access-token issuer in v1.7.1-beta1 from
// "casaos" to "zimaos"; both are accepted so older hosts keep working.
//
// Measured with live logins (ZFW project):
//
//	2026-06-12, v1.7.0-beta1: access iss="casaos", refresh iss="refresh"
//	2026-08-12, v1.7.1-beta1: access iss="zimaos", refresh iss="refresh"
var sessionIssuers = []string{"casaos", "zimaos"}

func isSessionIssuer(iss string) bool {
	for _, want := range sessionIssuers {
		if iss == want {
			return true
		}
	}
	return false
}

var b64 = base64.RawURLEncoding

type keyEntry struct {
	kid string
	pub *ecdsa.PublicKey
}

// Resolver returns the JWKS URL to fetch keys from.
type Resolver func(ctx context.Context) (string, error)

// Verifier checks ES256 JWTs against a cached, periodically refreshed JWKS.
type Verifier struct {
	resolve  Resolver
	http     *http.Client
	disabled bool

	mu      sync.RWMutex
	keys    []keyEntry
	fetched time.Time

	rejects rejectLog
}

// NewVerifier returns a Verifier that loads its keys from the URL the
// resolver yields. The HTTP client refuses redirects so a JWKS fetch cannot
// be bounced off-host.
func NewVerifier(resolve Resolver) *Verifier {
	return &Verifier{
		resolve: resolve,
		http: &http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("redirect during JWKS fetch refused")
			},
		},
	}
}

// Disabled returns a Verifier whose middleware lets everything through.
// It exists for local development without a ZimaOS user-service.
func Disabled() *Verifier {
	return &Verifier{disabled: true}
}

// StaticURL is a Resolver for a fixed JWKS URL (tests, manual override).
func StaticURL(u string) Resolver {
	return func(context.Context) (string, error) { return u, nil }
}

// JWKSResolver discovers the JWKS endpoint through the gateway's route
// table: <runtimePath>/management.url names the gateway management API,
// GET /v1/gateway/routes lists {path,target} pairs. The target is accepted
// only when it points at loopback — the table is mutable, and an off-host
// entry must not become the trust anchor for who may run root commands.
func JWKSResolver(runtimePath string) Resolver {
	client := &http.Client{Timeout: 5 * time.Second}
	return func(ctx context.Context) (string, error) {
		raw, err := os.ReadFile(filepath.Join(runtimePath, "management.url"))
		if err != nil {
			return "", fmt.Errorf("gateway management url: %w", err)
		}
		base := strings.TrimRight(strings.TrimSpace(string(raw)), "/")
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/gateway/routes", nil)
		if err != nil {
			return "", err
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("gateway routes: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return "", fmt.Errorf("gateway routes: HTTP %d", resp.StatusCode)
		}
		var routes []struct {
			Path   string `json:"path"`
			Target string `json:"target"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&routes); err != nil {
			return "", fmt.Errorf("gateway routes: %w", err)
		}
		for _, rt := range routes {
			if rt.Path != jwksRoute {
				continue
			}
			target := strings.TrimRight(rt.Target, "/") + jwksRoute
			if !isLoopbackURL(target) {
				return "", fmt.Errorf("JWKS target %q is not loopback — refusing off-host trust anchor", rt.Target)
			}
			return target, nil
		}
		return "", fmt.Errorf("route %q not registered at the gateway", jwksRoute)
	}
}

func isLoopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type jwk struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Kid string `json:"kid"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// refreshKeys resolves the JWKS URL, fetches the set and installs its
// EC/P-256 keys.
func (v *Verifier) refreshKeys(ctx context.Context) error {
	jwksURL, err := v.resolve(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURL, nil)
	if err != nil {
		return err
	}
	resp, err := v.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("JWKS HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(body, &set); err != nil {
		return err
	}
	var keys []keyEntry
	for _, k := range set.Keys {
		if k.Kty != "EC" || k.Crv != "P-256" {
			continue
		}
		x, errX := b64.DecodeString(k.X)
		y, errY := b64.DecodeString(k.Y)
		if errX != nil || errY != nil {
			continue
		}
		pub, err := p256PublicKey(x, y)
		if err != nil {
			continue
		}
		keys = append(keys, keyEntry{kid: k.Kid, pub: pub})
	}
	if len(keys) == 0 {
		return errors.New("JWKS contains no EC/P-256 key")
	}
	v.mu.Lock()
	v.keys, v.fetched = keys, time.Now()
	v.mu.Unlock()
	return nil
}

// p256PublicKey builds a P-256 key from JWK coordinate bytes. RFC 7518
// §6.2.1.2 fixes both at 32 bytes; shorter values (a leading zero dropped
// by the encoder) are accepted, longer ones and points off the curve are
// refused so a malformed JWKS entry is skipped, not installed.
func p256PublicKey(x, y []byte) (*ecdsa.PublicKey, error) {
	const size = 32
	if len(x) > size || len(y) > size {
		return nil, errors.New("coordinate longer than the curve size")
	}
	// uncompressed SEC 1 point: 0x04 || X || Y, each left-padded to 32 bytes;
	// the parser does the on-curve check (the X/Y fields and IsOnCurve are
	// deprecated since Go 1.25/1.26)
	point := make([]byte, 1+2*size)
	point[0] = 4
	copy(point[1+size-len(x):], x)
	copy(point[1+2*size-len(y):], y)
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), point)
	if err != nil {
		return nil, fmt.Errorf("point is not on P-256: %w", err)
	}
	return pub, nil
}

// currentKeys returns the cached keys, refreshing when stale or absent. A
// refresh failure is tolerated only while the cached set is younger than
// jwksMaxStale; past that the verifier fails closed.
func (v *Verifier) currentKeys() ([]keyEntry, error) {
	v.mu.RLock()
	keys, fetched := v.keys, v.fetched
	v.mu.RUnlock()
	if len(keys) > 0 && time.Since(fetched) < jwksTTL {
		return keys, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := v.refreshKeys(ctx); err != nil {
		if len(keys) > 0 && time.Since(fetched) < jwksMaxStale {
			return keys, nil
		}
		return nil, err
	}
	v.mu.RLock()
	keys = v.keys
	v.mu.RUnlock()
	return keys, nil
}

// refreshedKeys fetches the key set again when the cached one is older
// than jwksRetryFloor, and returns what is cached afterwards.
func (v *Verifier) refreshedKeys() ([]keyEntry, error) {
	v.mu.RLock()
	fetched := v.fetched
	v.mu.RUnlock()
	if time.Since(fetched) < jwksRetryFloor {
		return nil, errors.New("key set refreshed too recently")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := v.refreshKeys(ctx); err != nil {
		return nil, err
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.keys, nil
}

// Verify checks a raw JWT: ES256 header, r‖s signature over the signing
// input against a JWKS key (matched by kid when the token names one),
// accepted issuer, mandatory exp, and nbf with a small skew.
func (v *Verifier) Verify(token string) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return errors.New("not a JWT (three segments expected)")
	}
	hdrRaw, err := b64.DecodeString(parts[0])
	if err != nil {
		return errors.New("header not decodable")
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(hdrRaw, &hdr); err != nil {
		return errors.New("header not readable")
	}
	if hdr.Alg != "ES256" {
		return fmt.Errorf("alg %q not supported (only ES256)", hdr.Alg)
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return errors.New("signature invalid")
	}
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))

	keys, err := v.currentKeys()
	if err != nil {
		return fmt.Errorf("JWKS unavailable: %w", err)
	}
	matches := func(keys []keyEntry) bool {
		for _, k := range keys {
			if hdr.Kid != "" && k.kid != "" && k.kid != hdr.Kid {
				continue
			}
			if ecdsa.Verify(k.pub, digest[:], r, s) {
				return true
			}
		}
		return false
	}
	if !matches(keys) {
		// the key set may have rotated since it was fetched: refresh once and look again
		if keys, err = v.refreshedKeys(); err != nil || !matches(keys) {
			return errors.New("signature matches no JWKS key")
		}
	}

	plRaw, err := b64.DecodeString(parts[1])
	if err != nil {
		return errors.New("payload not decodable")
	}
	var claims struct {
		Iss string `json:"iss"`
		Exp int64  `json:"exp"`
		Nbf int64  `json:"nbf"`
	}
	if err := json.Unmarshal(plRaw, &claims); err != nil {
		return errors.New("payload not readable")
	}
	if !isSessionIssuer(claims.Iss) {
		return fmt.Errorf("token issuer %q not accepted (want one of %v)", claims.Iss, sessionIssuers)
	}
	if claims.Exp == 0 {
		return errors.New("token without expiry (exp)")
	}
	now := time.Now()
	if now.Unix() >= claims.Exp {
		return errors.New("token expired")
	}
	if claims.Nbf != 0 && now.Add(clockSkew).Unix() < claims.Nbf {
		return errors.New("token not yet valid (nbf)")
	}
	return nil
}

// rejectLogInterval is the minimum spacing between two "session rejected"
// log lines: rejections are attacker-triggerable, so they are rate-limited
// and the suppressed count is reported on the next line that gets through.
const rejectLogInterval = 30 * time.Second

type rejectLog struct {
	mu         sync.Mutex
	last       time.Time
	suppressed int
}

func (l *rejectLog) admit(now time.Time) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.last.IsZero() && now.Sub(l.last) < rejectLogInterval {
		l.suppressed++
		return false, 0
	}
	n := l.suppressed
	l.last, l.suppressed = now, 0
	return true, n
}

// logReject writes one rate-limited line for a refused request. The reason
// never contains the token, only what was wrong with it — when ZimaOS
// renamed the issuer, one such line would have named the cause outright.
func (v *Verifier) logReject(r *http.Request, reason string) {
	ok, suppressed := v.rejects.admit(time.Now())
	if !ok {
		return
	}
	log.Printf("[auth] session rejected: %s (path=%s client=%s suppressed_since_last=%d)",
		reason, r.URL.Path, clientAddr(r), suppressed)
}

// clientAddr names the requester. The daemon binds loopback behind the
// gateway, so the LAN client arrives in X-Forwarded-For; it is gateway-set
// and only used for the log line.
func clientAddr(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		if s := strings.TrimSpace(first); s != "" {
			return s
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// Middleware wraps next so every request must carry a valid ZimaOS bearer
// token. Failures answer 401 with a JSON body {"error","code"} so the UI
// can tell an expired session from a missing one.
func (v *Verifier) Middleware(next http.Handler) http.Handler {
	if v.disabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			v.logReject(r, "no bearer token")
			unauthorized(w, "auth_required", "authentication required")
			return
		}
		if err := v.Verify(token); err != nil {
			v.logReject(r, err.Error())
			unauthorized(w, "auth_invalid", "invalid session: "+err.Error())
			return
		}
		next.ServeHTTP(w, r)
	})
}

func unauthorized(w http.ResponseWriter, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg, "code": code})
}

func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}
