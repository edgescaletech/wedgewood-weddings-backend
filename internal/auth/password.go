package auth

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/mail"
	"strings"

	workos "github.com/workos/workos-go/v7"
)

// passwordProvider isolates password authentication for testing.
type passwordProvider interface {
	Password(context.Context, string, string) (*workos.AuthenticateResponse, error)
}

// PasswordLogin checks credentials with WorkOS and creates the usual local session.
func (server *Server) PasswordLogin(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")

	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", "POST")
		fail(writer, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Require the configured browser origin and JSON to prevent cross-site form submissions.
	if request.Header.Get("Origin") == "" || request.Header.Get("Origin") != strings.TrimRight(server.config.AppURL, "/") {
		fail(writer, http.StatusForbidden, "invalid origin")
		return
	}

	contentType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		fail(writer, http.StatusUnsupportedMediaType, "expected application/json")
		return
	}

	request.Body = http.MaxBytesReader(writer, request.Body, 16<<10)
	var credentials struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}

	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&credentials); err != nil {
		fail(writer, http.StatusBadRequest, "invalid request")
		return
	}

	credentials.Email = strings.TrimSpace(credentials.Email)
	address, err := mail.ParseAddress(credentials.Email)
	if err != nil || address.Address != credentials.Email || credentials.Password == "" || len(credentials.Password) > 1024 {
		fail(writer, http.StatusBadRequest, "invalid email or password")
		return
	}

	provider, ok := server.provider.(passwordProvider)
	if !ok {
		fail(writer, http.StatusServiceUnavailable, "authentication unavailable")
		return
	}

	response, err := provider.Password(request.Context(), credentials.Email, credentials.Password)
	
	if err != nil {
		// Never log WorkOS errors: they may contain pending authentication tokens.
		var apiError *workos.APIError
		var networkError *workos.NetworkError
		if errors.As(err, &networkError) {
			fail(writer, http.StatusServiceUnavailable, "authentication unavailable")
		} else if errors.As(err, &apiError) && apiError.StatusCode == http.StatusTooManyRequests {
			fail(writer, http.StatusTooManyRequests, "too many attempts; try again later")
		} else if errors.As(err, &apiError) && apiError.StatusCode >= 500 {
			fail(writer, http.StatusServiceUnavailable, "authentication unavailable")
		} else {
			fail(writer, http.StatusUnauthorized, "unable to sign in; check your credentials or contact your administrator")
		}
		return
	}

	if response == nil || response.User == nil {
		fail(writer, http.StatusUnauthorized, "unable to sign in")
		return
	}

	server.createSession(writer, request, response, true)
}
