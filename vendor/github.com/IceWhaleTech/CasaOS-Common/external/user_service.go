package external

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	"github.com/IceWhaleTech/CasaOS-Common/utils/jwt"
)

const (
	UserServiceAddressFilename = "user-service.url"
)

var (
	cachedPublicKey *ecdsa.PublicKey
	lastUpdate      time.Time
)

func GetPublicKey(runtimePath string) (*ecdsa.PublicKey, error) {
	if cachedPublicKey != nil && time.Since(lastUpdate) < 10*time.Second {
		return cachedPublicKey, nil
	}

	address, err := getAddress(filepath.Join(runtimePath, UserServiceAddressFilename))
	if err != nil {
		return nil, err
	}

	jwksURL, err := url.JoinPath(address, jwt.JWKSPath)
	if err != nil {
		return nil, err
	}

	resp, err := http.Get(jwksURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch JWKS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch JWKS: received status code %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read JWKS response: %w", err)
	}

	var jwks jwt.JWKS
	err = json.Unmarshal(body, &jwks)
	if err != nil {
		return nil, fmt.Errorf("failed to parse JWKS: %w", err)
	}

	// Use the first key in JWKS to validate the JWT
	if len(jwks.Keys) == 0 {
		return nil, fmt.Errorf("no keys found in JWKS")
	}

	key := jwks.Keys[0]
	xBytes, err := base64.RawURLEncoding.DecodeString(key.X)
	if err != nil {
		return nil, fmt.Errorf("failed to decode JWK x value: %w", err)
	}

	yBytes, err := base64.RawURLEncoding.DecodeString(key.Y)
	if err != nil {
		return nil, fmt.Errorf("failed to decode JWK y value: %w", err)
	}

	cachedPublicKey = &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(xBytes),
		Y:     new(big.Int).SetBytes(yBytes),
	}

	lastUpdate = time.Now()

	return cachedPublicKey, nil
}
