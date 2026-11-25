package auth

import (
	"strings"
	"testing"

	workos "github.com/workos/workos-go/v7"
)

func TestWorkOSPasswordError(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   workos.APIError
		reset bool
		link  bool
		want  string
	}{
		{"expired reset", workos.APIError{StatusCode: 400, ErrorCode: "password_reset_token_expired"}, true, true, "has expired"},
		{"used reset", workos.APIError{StatusCode: 400, Message: "Token already used"}, true, true, "already been used"},
		{"invalid reset", workos.APIError{StatusCode: 404}, true, true, "no longer valid"},
		{"breached password", workos.APIError{StatusCode: 422, FieldErrors: []workos.FieldError{{Field: "new_password", Code: "password_breached"}}}, true, false, "data breach"},
		{"weak invite password", workos.APIError{StatusCode: 422, Code: "password_strength_error", Message: "private provider details"}, false, false, "too easy to guess"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message, link := workOSPasswordError(&tc.err, tc.reset)
			if link != tc.link || !strings.Contains(message, tc.want) || strings.Contains(message, "private provider details") {
				t.Fatalf("message = %q, invalidLink = %t", message, link)
			}
		})
	}
}
