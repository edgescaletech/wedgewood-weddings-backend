package auth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
)

// cookie sets a protected browser cookie for authentication state or sessions.
func (server *Server) cookie(writer http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(
		writer,
		&http.Cookie{
			Name:     name,
			Value:    value,
			Path:     "/",
			HttpOnly: true,
			Secure:   server.config.Secure,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   maxAge,
		},
	)
}

// fail returns a generic JSON error without exposing internal details.
func fail(writer http.ResponseWriter, status int, message string) {
	// Authentication responses must not be cached or expose internal errors.
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]string{"error": message})
}

// random generates an unpredictable identifier for a browser session.
func random() (string, error) {
	// Use cryptographic randomness so session IDs cannot be guessed.
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	// URL-safe encoding makes the random bytes suitable for a cookie value.
	return base64.RawURLEncoding.EncodeToString(randomBytes), nil
}

// pending binds the OAuth state and PKCE verifier to the initiating browser.
type pending struct {
	State    string
	Verifier string
	Expires  int64
}
