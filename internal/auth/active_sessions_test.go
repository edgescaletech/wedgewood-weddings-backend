package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	workos "github.com/workos/workos-go/v7"
)

type sessionListMock struct {
	fakeProvider
	requestedUser string
	sessions      []workos.UserSessionsListItem
	err           error
}

func (mock *sessionListMock) ListSessions(_ context.Context, userID string) ([]workos.UserSessionsListItem, error) {
	mock.requestedUser = userID
	return mock.sessions, mock.err
}

func TestActiveSessionsScopesWorkOSToCurrentUser(t *testing.T) {
	agent := "Test browser on another device"
	mock := &sessionListMock{sessions: []workos.UserSessionsListItem{
		{ID: "current", UserID: "user_1", Status: "active"},
		{ID: "other", UserID: "user_1", Status: "active", UserAgent: &agent, CreatedAt: "2026-11-20T12:00:00Z"},
		{ID: "revoked", UserID: "user_1", Status: "revoked"},
		{ID: "unrelated", UserID: "user_2", Status: "active"},
	}}
	server := &Server{provider: mock}
	request := httptest.NewRequest(http.MethodGet, "/auth/sessions?userId=user_2", nil)
	request.Header.Set("User-Agent", "Current browser")
	request = request.WithContext(context.WithValue(request.Context(), contextKey{}, Identity{
		WorkOSID: "user_1", sessionID: "current",
	}))
	response := httptest.NewRecorder()
	server.ActiveSessions(response, request)
	var sessions []activeSession
	if err := json.Unmarshal(response.Body.Bytes(), &sessions); err != nil ||
		response.Code != http.StatusOK || mock.requestedUser != "user_1" || len(sessions) != 2 {
		t.Fatalf("unexpected session list: %d %s (%v)", response.Code, response.Body, err)
	}
	if !sessions[0].Current || sessions[0].UserAgent == nil || *sessions[0].UserAgent != "Current browser" ||
		sessions[1].Current || sessions[1].UserAgent == nil || *sessions[1].UserAgent != agent {
		t.Fatalf("incorrect session metadata: %s", response.Body)
	}
}

func TestActiveSessionsHandlesProviderFailure(t *testing.T) {
	server := &Server{provider: &sessionListMock{err: errors.New("provider details")}}
	request := httptest.NewRequest(http.MethodGet, "/auth/sessions", nil)
	request = request.WithContext(context.WithValue(request.Context(), contextKey{}, Identity{WorkOSID: "user_1"}))
	response := httptest.NewRecorder()
	server.ActiveSessions(response, request)
	if response.Code != http.StatusServiceUnavailable || response.Body.String() != "{\"error\":\"sessions unavailable\"}\n" {
		t.Fatalf("unexpected provider failure response: %d %s", response.Code, response.Body)
	}
}
