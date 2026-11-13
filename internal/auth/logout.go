package auth

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	workos "github.com/workos/workos-go/v7"
	"net/http"
	"strings"
)

// Logout deletes the local session and attempts to revoke the WorkOS session.
func (server *Server) Logout(writer http.ResponseWriter, request *http.Request) {

	if request.Method != http.MethodPost {
		fail(writer, 405, "method not allowed")
		return
	}

	// Require the trusted browser origin because logout changes session state.
	origin := request.Header.Get("Origin")
	if origin == "" || strings.TrimRight(origin, "/") != strings.TrimRight(server.config.AppURL, "/") {
		fail(writer, 403, "invalid origin")
		return
	}

	cookie, err := request.Cookie("session")
	if err != nil {
		fail(writer, 401, "authentication required")
		return
	}

	// Delete the local session first so the browser credential stops working.
	sessionHash := sha256.Sum256([]byte(cookie.Value))
	var sealed string
	err = server.database.QueryRowContext(request.Context(), `DELETE FROM auth_sessions WHERE id_hash=$1 RETURNING sealed_session`, sessionHash[:]).Scan(&sealed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		fail(writer, 503, "logout unavailable")
		return
	}

	// Local access ends immediately. The sealed session is authenticated with our
	// encryption key, so its session ID can be used for revocation even when the
	// access token has expired. It is never used to authenticate a request here.
	server.cookie(writer, "session", "", -1)
	if err == nil {
		session := workos.NewSession(nil, sealed, server.config.CookiePassword)
		result, sessionErr := session.Authenticate()
		if sessionErr != nil || result == nil || !result.Authenticated || result.SessionID == "" {
			fail(writer, http.StatusServiceUnavailable, "local session ended; remote logout failed")
			return
		}
		if server.provider.Revoke(request.Context(), result.SessionID) != nil {
			fail(writer, http.StatusServiceUnavailable, "local session ended; remote logout failed")
			return
		}
	}

	writer.WriteHeader(http.StatusNoContent)
}
