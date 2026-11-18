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

type provisionTransport func(*http.Request) (*http.Response, error)

func (transport provisionTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

// Verify the SDK sends the password to WorkOS and keeps email unverified.
func TestWorkOSIdentityProviderCreate(t *testing.T) {
	transport := provisionTransport(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodGet {
			return workOSResponse(http.StatusNotFound, `{}`), nil
		}
		if request.Method != http.MethodPost || request.URL.Path != "/user_management/users" {
			t.Error("unexpected WorkOS request")
		}
		var body struct {
			ExternalID    string `json:"external_id"`
			Email         string `json:"email"`
			FirstName     string `json:"first_name"`
			Password      string `json:"password"`
			EmailVerified *bool  `json:"email_verified"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.ExternalID != "admin-provision-op" || request.Header.Get("Idempotency-Key") != body.ExternalID || body.Email != "person@example.com" || body.FirstName != "Example Person" || body.Password != "initial-test-password" || body.EmailVerified == nil || *body.EmailVerified {
			t.Error("incorrect WorkOS provisioning payload")
		}
		return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"user_created","external_id":"admin-provision-op","email":"person@example.com"}`))}, nil
	})
	provider := WorkOSIdentityProvider{Client: workos.NewClient("test-key", workos.WithHTTPClient(&http.Client{Transport: transport}))}
	id, err := provider.Create(context.Background(), "op", "person@example.com", "Example Person", "initial-test-password")
	if err != nil || id != "user_created" {
		t.Fatalf("provision failed: id=%q error=%v", id, err)
	}
}

// Do not expose upstream responses, which can contain credentials.
func TestWorkOSIdentityProviderHidesUpstreamErrors(t *testing.T) {
	transport := provisionTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnprocessableEntity, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"message":"initial-test-password"}`))}, nil
	})
	provider := WorkOSIdentityProvider{Client: workos.NewClient("test-key", workos.WithHTTPClient(&http.Client{Transport: transport}))}
	_, err := provider.Create(context.Background(), "op", "person@example.com", "Example Person", "initial-test-password")
	if err == nil || strings.Contains(err.Error(), "initial-test-password") {
		t.Fatal("upstream error was not safely handled")
	}
}

func workOSResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestWorkOSReconciliationRequiresExactExternalID(t *testing.T) {
	for _, externalID := range []string{"admin-provision-op", "someone-else"} {
		t.Run(externalID, func(t *testing.T) {
			transport := provisionTransport(func(request *http.Request) (*http.Response, error) {
				if request.Method != http.MethodGet || request.URL.Path != "/user_management/users/external_id/admin-provision-op" {
					t.Fatal("retry must resolve by the operation's external ID")
				}
				return workOSResponse(http.StatusOK, `{"id":"user_existing","email":"person@example.com","external_id":"`+externalID+`"}`), nil
			})
			provider := WorkOSIdentityProvider{Client: workos.NewClient("test-key", workos.WithHTTPClient(&http.Client{Transport: transport}))}
			id, err := provider.Create(context.Background(), "op", "person@example.com", "Example Person", "initial-test-password")
			if externalID == "admin-provision-op" {
				if err != nil || id != "user_existing" {
					t.Fatalf("verified retry: %q %v", id, err)
				}
			} else if err == nil || id != "" {
				t.Fatal("email match alone reconciled an unrelated identity")
			}
		})
	}
}
