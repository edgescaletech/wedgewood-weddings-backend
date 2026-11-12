package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"github.com/google/uuid"
	workos "github.com/workos/workos-go/v7"
	"net/http"
	"time"
)

// Login redirects the browser to AuthKit after storing the encrypted OAuth state.
func (server *Server) Login(writer http.ResponseWriter, request *http.Request) {

	// Login is a browser navigation; reject other HTTP methods.
	if request.Method != http.MethodGet {
		fail(writer, 405, "method not allowed")
		return
	}

	// Ask WorkOS for a login URL along with state and a PKCE verifier.
	p, err := server.provider.Start(server.config.RedirectURI)
	if err != nil {
		fail(writer, 503, "authentication unavailable")
		return
	}

	// Store the state and verifier together so the callback is bound to this browser.
	pendingLogin := pending{
		State:    p.State,
		Verifier: p.CodeVerifier,
		Expires:  time.Now().Add(10 * time.Minute).Unix(),
	}

	sealed, err := workos.Seal(pendingLogin, server.config.CookiePassword)
	if err != nil {
		fail(writer, 500, "authentication unavailable")
		return
	}

	// Save the encrypted state briefly, then send the browser to AuthKit.
	server.cookie(writer, "auth_state", sealed, 600)
	writer.Header().Set("Cache-Control", "no-store")
	http.Redirect(writer, request, p.URL, http.StatusFound)
}

// Callback validates OAuth state, exchanges the code, and creates a local session.
func (server *Server) Callback(writer http.ResponseWriter, request *http.Request) {

	// Avoid caching the callback or leaking its authorization code in a referrer.
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Referrer-Policy", "no-referrer")

	if request.Method != http.MethodGet {
		fail(writer, 405, "method not allowed")
		return
	}

	// Read and immediately clear the short-lived browser state cookie.
	cookie, err := request.Cookie("auth_state")
	server.cookie(writer, "auth_state", "", -1)
	if err != nil {
		fail(writer, 400, "invalid authentication state")
		return
	}

	// Unseal the original state and compare it with the callback in constant time.
	p, err := workos.Unseal[pending](cookie.Value, server.config.CookiePassword)
	state := request.URL.Query().Get("state")
	if err != nil || p.Expires <= time.Now().Unix() || state == "" || subtle.ConstantTimeCompare([]byte(p.State), []byte(state)) != 1 || request.URL.Query().Get("code") == "" {
		fail(writer, 400, "invalid authentication state")
		return
	}

	// Exchange the one-time code using the PKCE verifier saved at login.
	response, err := server.provider.Exchange(request.Context(), request.URL.Query().Get("code"), p.Verifier)
	if err != nil || response == nil || response.User == nil {
		fail(writer, 401, "authentication failed")
		return
	}

	server.createSession(writer, request, response, false)
}

// createSession verifies the WorkOS response and stores the linked local session.
func (server *Server) createSession(writer http.ResponseWriter, request *http.Request, response *workos.AuthenticateResponse, jsonResponse bool) {
	// Trust the WorkOS user only after verifying the token and matching its subject.
	c, err := server.verify(request.Context(), response.AccessToken)
	if err != nil || c.Subject != response.User.ID || response.User.Email == "" || response.RefreshToken == "" {
		fail(writer, 401, "authentication failed")
		return
	}

	// Encrypt the WorkOS tokens before storing them in PostgreSQL.
	sealed, err := workos.SealSessionFromAuthResponse(
		response.AccessToken,
		response.RefreshToken,
		response.User,
		response.Impersonator,
		server.config.CookiePassword,
	)

	if err != nil {
		fail(writer, 500, "authentication failed")
		return
	}

	sessionID, err := random()
	if err != nil {
		fail(writer, 500, "authentication failed")
		return
	}

	// Store only a hash of the browser credential; WorkOS tokens remain encrypted.
	sessionHash := sha256.Sum256([]byte(sessionID))
	transaction, err := server.database.BeginTx(request.Context(), nil)
	if err != nil {
		fail(writer, 503, "authentication unavailable")
		return
	}

	defer transaction.Rollback()
	// A new user receives the database default (inactive); existing activation is preserved.
	_, err = transaction.ExecContext(
		request.Context(),
		`INSERT INTO users (id,workos_id,email) VALUES ($1,$2,$3)
   ON CONFLICT (workos_id) DO UPDATE SET email=EXCLUDED.email, updated_at=now()`,
		uuid.NewString(),
		c.Subject,
		response.User.Email,
	)

	if err == nil {
		_, err = transaction.ExecContext(
			request.Context(),
			`INSERT INTO auth_sessions (id_hash,workos_id,sealed_session,expires_at) VALUES ($1,$2,$3,$4)`,
			sessionHash[:],
			c.Subject,
			sealed,
			time.Now().Add(24*time.Hour),
		)
	}

	// The user and session must be committed together before setting the cookie.
	if err != nil || transaction.Commit() != nil {
		fail(writer, 503, "authentication unavailable")
		return
	}

	// Give the browser the opaque session ID and return it to the application.
	server.cookie(writer, "session", sessionID, 86400)
	if jsonResponse {
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(writer, request, server.config.AppURL, http.StatusSeeOther)
}
