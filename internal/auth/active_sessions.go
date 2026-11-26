package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	workos "github.com/workos/workos-go/v7"
)

type sessionListProvider interface {
	ListSessions(context.Context, string) ([]workos.UserSessionsListItem, error)
}

type activeSession struct {
	ID        string  `json:"id"`
	UserAgent *string `json:"userAgent,omitempty"`
	CreatedAt string  `json:"createdAt"`
	ExpiresAt string  `json:"expiresAt"`
	Current   bool    `json:"current"`
}

// ActiveSessions lists WorkOS sessions for the authenticated local account.
func (server *Server) ActiveSessions(writer http.ResponseWriter, request *http.Request) {
	user, found := User(request.Context())
	if !found {
		fail(writer, http.StatusUnauthorized, "authentication required")
		return
	}
	provider, ok := server.provider.(sessionListProvider)
	if !ok {
		fail(writer, http.StatusServiceUnavailable, "sessions unavailable")
		return
	}

	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	sessions, err := provider.ListSessions(ctx, user.WorkOSID)
	if err != nil {
		fail(writer, http.StatusServiceUnavailable, "sessions unavailable")
		return
	}

	active := make([]activeSession, 0, len(sessions))
	for _, session := range sessions {
		if session.UserID != user.WorkOSID || session.Status != "active" || session.ID == "" {
			continue
		}
		current := session.ID == user.sessionID
		userAgent := session.UserAgent
		if current && (userAgent == nil || *userAgent == "") {
			requestAgent := request.UserAgent()
			userAgent = &requestAgent
		}
		active = append(active, activeSession{
			ID: session.ID, UserAgent: userAgent, CreatedAt: session.CreatedAt,
			ExpiresAt: session.ExpiresAt, Current: current,
		})
	}

	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(active)
}
