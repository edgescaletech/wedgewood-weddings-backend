package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/mail"
	"strings"

	workos "github.com/workos/workos-go/v7"
)

// signupProvider keeps temporary account creation separate and testable.
type signupProvider interface {
	CreateUser(context.Context, string, string) error
}

// CreateUser asks WorkOS to create an unverified password account.
func (provider workOS) CreateUser(ctx context.Context, email, password string) error {
	verified := false
	_, err := provider.c.UserManagement().Create(ctx, &workos.UserManagementCreateParams{
		Email:         email,
		EmailVerified: &verified,
		Password:      workos.UserManagementPasswordPlaintext{Password: password},
	})
	return err
}

// Signup creates a WorkOS identity; normal sign-in verifies and links the user.
func (server *Server) Signup(writer http.ResponseWriter, request *http.Request) {
	var credentials struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !server.decodeRecoveryRequest(writer, request, &credentials) {
		return
	}

	credentials.Email = strings.TrimSpace(credentials.Email)
	address, err := mail.ParseAddress(credentials.Email)
	if err != nil || address.Address != credentials.Email || credentials.Password == "" || len(credentials.Password) > 1024 {
		fail(writer, http.StatusBadRequest, "invalid email or password")
		return
	}

	provider, ok := server.provider.(signupProvider)
	if !ok {
		fail(writer, http.StatusServiceUnavailable, "signup temporarily unavailable")
		return
	}

	// WorkOS errors can contain secrets, so record only the numeric status and
	// known validation fields; never log the response, credentials, or email.
	if err := provider.CreateUser(request.Context(), credentials.Email, credentials.Password); err != nil {
		var apiError *workos.APIError
		if !errors.As(err, &apiError) {
			log.Print("signup failed: WorkOS request unavailable")
			fail(writer, http.StatusServiceUnavailable, "signup temporarily unavailable")
			return
		}

		passwordRejected, emailRejected := false, false
		for _, fieldError := range apiError.FieldErrors {
			switch fieldError.Field {
			case "password":
				passwordRejected = true
			case "email":
				emailRejected = true
			}
		}
		log.Printf("signup rejected by WorkOS: status=%d email_field=%t password_field=%t", apiError.StatusCode, emailRejected, passwordRejected)

		if apiError.StatusCode == http.StatusUnauthorized || apiError.StatusCode == http.StatusForbidden ||
			apiError.StatusCode >= 500 || apiError.StatusCode == http.StatusTooManyRequests || apiError.StatusCode == http.StatusRequestTimeout {
			fail(writer, http.StatusServiceUnavailable, "signup temporarily unavailable; contact your administrator")
			return
		}
		// Return WorkOS validation messages, but never echo submitted secrets.
		if message := signupValidationMessage(apiError, credentials.Password, server.config.APIKey, server.config.CookiePassword); message != "" {
			fail(writer, http.StatusBadRequest, message)
			return
		}
		fail(writer, http.StatusBadRequest, "unable to create account; check the email and password, or try signing in if you already have an account")
		return
	}

	writer.WriteHeader(http.StatusNoContent)
}

// signupValidationMessage extracts user-facing validation text from WorkOS.
// SDK v7 retains structured field errors in RawBody rather than FieldErrors.
func signupValidationMessage(apiError *workos.APIError, secrets ...string) string {
	if apiError.StatusCode != http.StatusBadRequest && apiError.StatusCode != http.StatusConflict && apiError.StatusCode != http.StatusUnprocessableEntity {
		return ""
	}

	var response struct {
		Message string `json:"message"`
		Errors  []struct {
			Field   string `json:"field"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	_ = json.Unmarshal([]byte(apiError.RawBody), &response)
	var messages []string
	for _, fieldError := range response.Errors {
		if fieldError.Field == "password" || fieldError.Field == "email" {
			messages = append(messages, fieldError.Message)
		}
	}
	if len(messages) == 0 {
		messages = append(messages, response.Message, apiError.Message)
	}

	for _, message := range messages {
		if strings.TrimSpace(message) == "" || len(message) > 500 {
			continue
		}
		safe := true
		for _, secret := range secrets {
			if secret != "" && strings.Contains(message, secret) {
				safe = false
				break
			}
		}
		if safe {
			return message
		}
	}
	return ""
}
