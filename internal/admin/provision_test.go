package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	workos "github.com/workos/workos-go/v7"
)

func TestWorkOSIdentityProviderSendsInvitation(t *testing.T) {
	transport := testTransport(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/user_management/invitations" ||
			request.Header.Get("Idempotency-Key") != "admin-invite-operation" {
			t.Errorf("unexpected invitation request: %s %s", request.Method, request.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || len(body) != 1 || body["email"] != "person@example.com" {
			t.Errorf("unexpected invitation body: %v (%v)", body, err)
		}
		return &http.Response{StatusCode: 201, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"inv_1","email":"person@example.com"}`))}, nil
	})
	provider := WorkOSIdentityProvider{Client: workos.NewClient("test-key", workos.WithHTTPClient(&http.Client{Transport: transport}))}
	invite, err := provider.Send(context.Background(), "person@example.com", "admin-invite-operation")
	if err != nil || invite == nil || invite.ID != "inv_1" {
		t.Fatalf("invite: %v %v", invite, err)
	}
}
