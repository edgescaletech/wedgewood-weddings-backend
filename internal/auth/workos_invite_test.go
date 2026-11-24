package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	workos "github.com/workos/workos-go/v7"
)

type inviteTransport func(*http.Request) (*http.Response, error)

func (transport inviteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestWorkOSInvitationUsesMagicAuthAndUpdatesVerifiedUser(t *testing.T) {
	type call struct {
		method, path, response string
		body                   map[string]string
	}
	calls := []call{
		{"POST", "/user_management/magic_auth", `{"id":"magic_auth_test"}`,
			map[string]string{"email": inviteEmail, "invitation_token": inviteToken}},
		{"POST", "/user_management/authenticate", `{"user":{"id":"user_invited"},"access_token":"test-token"}`,
			map[string]string{"client_id": "test-client", "client_secret": "test-key", "grant_type": "urn:workos:oauth:grant-type:magic-auth:code", "code": "123456", "email": inviteEmail, "invitation_token": inviteToken}},
		{"PUT", "/user_management/users/" + inviteUser, `{"id":"user_invited"}`,
			map[string]string{"password": invitePass}},
	}
	httpClient := &http.Client{Transport: inviteTransport(func(request *http.Request) (*http.Response, error) {
		if len(calls) == 0 {
			t.Fatal("unexpected WorkOS request")
		}
		expected := calls[0]
		calls = calls[1:]
		var body map[string]string
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if request.Method != expected.method || request.URL.Path != expected.path || !reflect.DeepEqual(body, expected.body) {
			t.Fatalf("incorrect WorkOS route or request properties for %s", expected.path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(expected.response))}, nil
	})}
	provider := workOS{workos.NewClient("test-key", workos.WithClientID("test-client"), workos.WithHTTPClient(httpClient))}
	ctx := context.Background()
	token := inviteToken
	if err := provider.SendInviteCode(ctx, inviteEmail, &token); err != nil {
		t.Fatal(err)
	}
	response, err := provider.VerifyInviteCode(ctx, inviteEmail, "123456", &token)
	if err != nil || response.User.ID != inviteUser {
		t.Fatal("could not verify invitation")
	}
	user, err := provider.SetInvitePassword(ctx, response.User.ID, invitePass)
	if err != nil || user.ID != inviteUser || len(calls) != 0 {
		t.Fatal("could not set verified user's password")
	}
}
