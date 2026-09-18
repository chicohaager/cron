package jwt

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strconv"

	"github.com/IceWhaleTech/CasaOS-Common/model"
	"github.com/IceWhaleTech/CasaOS-Common/utils/common_err"
	echojwt "github.com/labstack/echo-jwt/v4"
	"github.com/labstack/echo/v4"
	echo_middleware "github.com/labstack/echo/v4/middleware"
)

type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

type JWKS struct {
	Keys []JWK `json:"keys"`
}

const JWKSPath = ".well-known/jwks.json"

func JWT(publicKeyFunc func() (*ecdsa.PublicKey, error)) echo.MiddlewareFunc {
	return echojwt.WithConfig(
		echojwt.Config{
			Skipper: func(c echo.Context) bool {
				return c.RealIP() == "::1" || c.RealIP() == "127.0.0.1"
			},
			ParseTokenFunc: func(c echo.Context, token string) (interface{}, error) {
				valid, claims, err := Validate(token, publicKeyFunc)
				if err != nil || !valid {
					message := "token is invalid"
					c.JSON(http.StatusUnauthorized, model.Result{Success: common_err.ERROR_AUTH_TOKEN, Message: message})
					return nil, echo.ErrUnauthorized
				}
				c.Request().Header.Set("user_id", strconv.Itoa(claims.ID))

				return claims, nil
			},
			TokenLookupFuncs: []echo_middleware.ValuesExtractor{
				func(c echo.Context) ([]string, error) {
					if len(c.Request().Header.Get(echo.HeaderAuthorization)) > 0 {
						return []string{c.Request().Header.Get(echo.HeaderAuthorization)}, nil
					}
					return []string{c.QueryParam("token")}, nil
				},
			},
		},
	)
}

func GenerateKeyPair() (*ecdsa.PrivateKey, *ecdsa.PublicKey, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("error generating key pair: %w", err)
	}

	publicKey := &privateKey.PublicKey

	return privateKey, publicKey, nil
}

func GenerateJwksJSON(publicKey *ecdsa.PublicKey) ([]byte, error) {
	jwk := JWK{
		Kty: "EC",
		Crv: "P-256",
		X:   base64.RawURLEncoding.EncodeToString(publicKey.X.Bytes()),
		Y:   base64.RawURLEncoding.EncodeToString(publicKey.Y.Bytes()),
	}

	jwks := JWKS{
		Keys: []JWK{jwk},
	}

	return json.Marshal(jwks)
}

func PublicKeyFromJwksJSON(jwksJSON []byte) (*ecdsa.PublicKey, error) {
	var jwks JWKS
	err := json.Unmarshal(jwksJSON, &jwks)
	if err != nil {
		return nil, fmt.Errorf("error unmarshalling JWKS JSON: %w", err)
	}

	if len(jwks.Keys) == 0 {
		return nil, fmt.Errorf("no keys in JWKS")
	}

	jwk := jwks.Keys[0]

	x, err := base64.RawURLEncoding.DecodeString(jwk.X)
	if err != nil {
		return nil, fmt.Errorf("error decoding X: %w", err)
	}

	y, err := base64.RawURLEncoding.DecodeString(jwk.Y)
	if err != nil {
		return nil, fmt.Errorf("error decoding Y: %w", err)
	}

	publicKey := &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(x),
		Y:     new(big.Int).SetBytes(y),
	}

	return publicKey, nil
}

func JWKSHandler(jwksJSON []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, err := w.Write(jwksJSON)
		if err != nil {
			http.Error(w, "Error writing JWKS JSON", http.StatusInternalServerError)
			return
		}
	})
}
