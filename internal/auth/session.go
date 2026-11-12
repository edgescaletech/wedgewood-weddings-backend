package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/golang-jwt/jwt/v5"
	workos "github.com/workos/workos-go/v7"
	"net/http"
)

// Identity represents the local user linked to a WorkOS identity.
type Identity struct {
	ID       string `json:"id"`
	WorkOSID string `json:"workosId"`
	Email    string `json:"email"`
	IsActive bool   `json:"isActive"`
}

type contextKey struct{}

// User retrieves the authenticated local identity from the request context.
func User(ctx context.Context) (Identity, bool) {
	// The middleware stores the identity under an unexported context key.
	identity, found := ctx.Value(contextKey{}).(Identity)
	return identity, found
}

// session validates the WorkOS token and checks that the local user is active.
func (server *Server) session(writer http.ResponseWriter, request *http.Request) (Identity, bool) {

	// Read the browser credential and hash it to find the server-side session.
	var user Identity
	cookie, err := request.Cookie("session")
	if err != nil || cookie.Value == "" {
		fail(writer, 401, "authentication required")
		return user, false
	}
	sessionHash := sha256.Sum256([]byte(cookie.Value))

	transaction, err := server.database.BeginTx(request.Context(), nil)
	if err != nil {
		fail(writer, 503, "authentication unavailable")
		return user, false
	}

	// The row lock serializes refresh-token rotation across concurrent requests.
	defer transaction.Rollback()
	var sealed string
	err = transaction.QueryRowContext(
		request.Context(),
		`SELECT users.id, users.workos_id, users.email, users.is_active, sessions.sealed_session
   FROM auth_sessions AS sessions
   JOIN users ON users.workos_id = sessions.workos_id
   WHERE sessions.id_hash = $1 AND sessions.expires_at > now()
   FOR UPDATE OF sessions`,
		sessionHash[:],
	).Scan(
		&user.ID,
		&user.WorkOSID,
		&user.Email,
		&user.IsActive,
		&sealed,
	)

	// Missing and expired sessions cannot authenticate the request.
	if errors.Is(err, sql.ErrNoRows) {
		server.cookie(writer, "session", "", -1)
		fail(writer, 401, "authentication required")
		return user, false
	}

	if err != nil {
		fail(writer, 503, "authentication unavailable")
		return user, false
	}

	// WorkOS sign-in alone does not activate a local account.
	if !user.IsActive {
		fail(writer, 403, "account inactive")
		return user, false
	}

	// Decrypt the stored tokens and verify the access token before trusting it.
	data, err := workos.Unseal[workos.SessionData](sealed, server.config.CookiePassword)
	if err != nil || data.User == nil {
		fail(writer, 401, "invalid session")
		return user, false
	}

	c, err := server.verify(request.Context(), data.AccessToken)

	// Expired access tokens must be refreshed and the replacement verified.
	if err != nil && errors.Is(err, jwt.ErrTokenExpired) {

		response, refreshErr := server.provider.Refresh(request.Context(), data.RefreshToken)
		if refreshErr != nil || response == nil || response.User == nil || response.RefreshToken == "" {
			fail(writer, 401, "session expired")
			return user, false
		}

		// Check the replacement token and ensure it belongs to the same local user.
		c, refreshErr = server.verify(request.Context(), response.AccessToken)
		if refreshErr != nil || c.Subject != user.WorkOSID || response.User.ID != user.WorkOSID {
			fail(writer, 401, "invalid session")
			return user, false
		}

		sealed, refreshErr = workos.SealSessionFromAuthResponse(
			response.AccessToken,
			response.RefreshToken,
			response.User,
			response.Impersonator,
			server.config.CookiePassword,
		)

		if refreshErr != nil {
			fail(writer, 503, "authentication unavailable")
			return user, false
		}

		// Persist the rotated tokens while the session row remains locked.
		if _, refreshErr = transaction.ExecContext(request.Context(), `UPDATE auth_sessions SET sealed_session=$1 WHERE id_hash=$2`, sealed, sessionHash[:]); refreshErr != nil {
			fail(writer, 503, "authentication unavailable")
			return user, false
		}
		err = nil
	}

	// The verified token, encrypted user and local record must identify the same user.
	if err != nil || c.Subject != user.WorkOSID || data.User.ID != user.WorkOSID {
		fail(writer, 401, "invalid session")
		return user, false
	}

	// Commit any refreshed tokens before allowing the request to proceed.
	if transaction.Commit() != nil {
		fail(writer, 503, "authentication unavailable")
		return user, false
	}
	return user, true
}

// Middleware checks the session and adds the authenticated identity to the request.
func (server *Server) Middleware(next http.Handler) http.Handler {

	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {

		writer.Header().Set("Cache-Control", "no-store")

		// Only attach an identity and call the protected handler after validation.
		identity, found := server.session(writer, request)
		if found {
			next.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), contextKey{}, identity)))
		}
	})
}

// Me returns the authenticated local identity as JSON.
func (server *Server) Me(writer http.ResponseWriter, request *http.Request) {

	if request.Method != http.MethodGet {
		fail(writer, 405, "method not allowed")
		return
	}

	// Middleware supplies this identity; a missing value is unauthenticated.
	identity, found := User(request.Context())
	if !found {
		fail(writer, 401, "authentication required")
		return
	}

	writer.Header().Set("Content-Type", "application/json")

	_ = json.NewEncoder(writer).Encode(identity)
}
