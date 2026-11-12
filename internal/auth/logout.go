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

	// Clear the browser cookie and, when possible, revoke the WorkOS session.
	server.cookie(writer, "session", "", -1)
	if err == nil {
		data, refreshErr := workos.Unseal[workos.SessionData](sealed, server.config.CookiePassword)
		if refreshErr == nil {
			c, refreshErr := server.verify(request.Context(), data.AccessToken)
			if refreshErr == nil {
				if server.provider.Revoke(request.Context(), c.SessionID) != nil {
					fail(writer, 503, "local session ended; remote logout failed")
					return
				}
			}
		}
	}

	writer.WriteHeader(http.StatusNoContent)
}
