package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// claims contains only the WorkOS session claims needed to identify a user.
// RegisteredClaims supplies standard fields such as issuer, audience and expiry.
type claims struct {
	ClientID  string `json:"client_id"`
	SessionID string `json:"sid"`
	jwt.RegisteredClaims
}

var errJWKSUnavailable = errors.New("WorkOS signing keys unavailable")

// verifier retrieves WorkOS signing keys and keeps them briefly in memory.
// The mutex protects both the cached keys and their fetch timestamp.
type verifier struct {
	url      string
	issuer   string
	clientID string
	client   *http.Client
	mu       sync.Mutex
	keys     map[string]*rsa.PublicKey
	fetched  time.Time
}

// verify validates the signature and standard claims before returning identity data.
// WorkOS first-party session tokens identify the application with client_id; they
// may omit aud, but an audience supplied by WorkOS must also match our client ID.
func (tokenVerifier *verifier) verify(ctx context.Context, accessToken string) (*claims, error) {
	// Parse the token only with a WorkOS public key and the expected algorithm.
	verifiedClaims := &claims{}
	_, err := jwt.ParseWithClaims(accessToken, verifiedClaims, func(token *jwt.Token) (any, error) {
		// Never select an algorithm based only on the untrusted JWT header.
		if token.Method != jwt.SigningMethodRS256 {
			return nil, errors.New("unsupported signing algorithm")
		}
		keyID, found := token.Header["kid"].(string)
		if !found || keyID == "" {
			return nil, errors.New("missing key id")
		}
		return tokenVerifier.key(ctx, keyID)
	},
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithIssuer(tokenVerifier.issuer),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	)
	if err != nil {
		return nil, err
	}

	// Require the user, WorkOS session, application and issued-at timestamp.
	if verifiedClaims.ClientID != tokenVerifier.clientID || verifiedClaims.Subject == "" ||
		verifiedClaims.SessionID == "" || verifiedClaims.IssuedAt == nil {
		return nil, errors.New("invalid session claims")
	}
	// Some WorkOS session tokens omit audience; if present, it must match.
	if len(verifiedClaims.Audience) > 0 {
		audienceMatches := false
		for _, audience := range verifiedClaims.Audience {
			if audience == tokenVerifier.clientID {
				audienceMatches = true
				break
			}
		}
		if !audienceMatches {
			return nil, errors.New("invalid audience")
		}
	}
	return verifiedClaims, nil
}

// jwksResponse describes the public-key fields needed to verify RS256 signatures.
type jwksResponse struct {
	Keys []struct {
		KeyType   string `json:"kty"`
		KeyID     string `json:"kid"`
		Modulus   string `json:"n"`
		Exponent  string `json:"e"`
		Algorithm string `json:"alg"`
		Use       string `json:"use"`
	} `json:"keys"`
}

// key reloads the WorkOS JWKS when the cache expires or an unfamiliar key ID
// appears. This lets WorkOS rotate keys without requiring a server restart.
func (tokenVerifier *verifier) key(ctx context.Context, keyID string) (*rsa.PublicKey, error) {
	tokenVerifier.mu.Lock()
	defer tokenVerifier.mu.Unlock()

	// Reuse a recent key when its ID matches the token header.
	if cachedKey := tokenVerifier.keys[keyID]; cachedKey != nil && time.Since(tokenVerifier.fetched) < time.Hour {
		return cachedKey, nil
	}

	// Fetch the current key set when the cache is stale or the key has rotated.
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenVerifier.url, nil)
	if err != nil {
		return nil, err
	}
	response, err := tokenVerifier.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errJWKSUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errJWKSUnavailable
	}

	// Limit the response size before decoding an external service's JSON.
	var keySet jwksResponse
	if err := json.NewDecoder(http.MaxBytesReader(nil, response.Body, 1<<20)).Decode(&keySet); err != nil {
		return nil, fmt.Errorf("%w: invalid key set", errJWKSUnavailable)
	}
	// Convert eligible RSA signing keys from JWKS into Go public keys.
	publicKeys := make(map[string]*rsa.PublicKey)
	for _, candidate := range keySet.Keys {
		if candidate.KeyType != "RSA" || (candidate.Use != "" && candidate.Use != "sig") ||
			(candidate.Algorithm != "" && candidate.Algorithm != "RS256") {
			continue
		}

		modulusBytes, modulusError := base64.RawURLEncoding.DecodeString(candidate.Modulus)
		exponentBytes, exponentError := base64.RawURLEncoding.DecodeString(candidate.Exponent)
		if modulusError != nil || exponentError != nil || len(exponentBytes) == 0 || len(exponentBytes) > 4 {
			continue
		}
		// The RSA exponent is encoded as an unsigned big-endian integer.
		exponent := 0
		for _, exponentByte := range exponentBytes {
			exponent = exponent<<8 | int(exponentByte)
		}
		modulus := new(big.Int).SetBytes(modulusBytes)
		if modulus.BitLen() < 2048 || exponent < 3 {
			continue
		}
		publicKeys[candidate.KeyID] = &rsa.PublicKey{N: modulus, E: exponent}
	}

	// Replace the cache as a unit and return only the key requested by the JWT.
	tokenVerifier.keys = publicKeys
	tokenVerifier.fetched = time.Now()
	if publicKey := publicKeys[keyID]; publicKey != nil {
		return publicKey, nil
	}
	return nil, errors.New("unknown signing key")
}
