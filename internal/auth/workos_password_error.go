package auth

import (
	"errors"
	"strings"

	workos "github.com/workos/workos-go/v7"
)

// Classify provider responses without sending raw WorkOS text or request data to the browser.
func workOSPasswordError(err error, reset bool) (message string, invalidLink bool) {
	var apiError *workos.APIError
	if !errors.As(err, &apiError) || apiError.StatusCode >= 500 {
		return "", false
	}

	details := []string{apiError.Code, apiError.ErrorCode, apiError.Message, apiError.ErrorDescription}
	details = append(details, apiError.Errors...)
	passwordField := false
	for _, field := range apiError.FieldErrors {
		if field.Field == "password" || field.Field == "new_password" {
			passwordField = true
			details = append(details, field.Code)
		}
	}
	reason := strings.ToLower(strings.NewReplacer("_", " ", "-", " ").Replace(strings.Join(details, " ")))
	if reset && (apiError.StatusCode == 404 || apiError.StatusCode == 410 || strings.Contains(reason, "token") || strings.Contains(reason, "reset link") || strings.Contains(reason, "reset expired")) {
		switch {
		case strings.Contains(reason, "expired"):
			return "This reset link has expired. Request a new password reset link.", true
		case strings.Contains(reason, "already used"), strings.Contains(reason, "consumed"):
			return "This reset link has already been used. Request a new password reset link.", true
		case apiError.StatusCode == 404, apiError.StatusCode == 410, strings.Contains(reason, "invalid"), strings.Contains(reason, "not found"):
			return "This reset link is no longer valid. Request a new password reset link.", true
		}
	}

	switch {
	case strings.Contains(reason, "breach"), strings.Contains(reason, "compromised"), strings.Contains(reason, "pwned"):
		return "This password appeared in a data breach. Choose a different password.", false
	case strings.Contains(reason, "previously used"), strings.Contains(reason, "password history"), strings.Contains(reason, "password reused"):
		return "You’ve already used this password. Choose a different password.", false
	case strings.Contains(reason, "weak"), strings.Contains(reason, "strength"), strings.Contains(reason, "complexity"):
		return "This password is too easy to guess. Try a longer password or use Generate.", false
	case passwordField, strings.Contains(reason, "password policy"), strings.Contains(reason, "invalid password"):
		return "This password doesn’t meet the password policy. Try a different password or use Generate.", false
	default:
		return "", false
	}
}
