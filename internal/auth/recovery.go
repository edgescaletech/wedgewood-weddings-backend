package auth

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/mail"
	"strings"
	"time"

	workos "github.com/workos/workos-go/v7"
)

const verificationCookie = "email_verification"

type recoveryProvider interface {
	VerifyEmail(context.Context, string, string) (*workos.AuthenticateResponse, error)
	RequestPasswordReset(context.Context, string) error
	ConfirmPasswordReset(context.Context, string, string) (*workos.ResetPasswordResponse, error)
}

type verificationState struct {
	Token   string `json:"token"`
	Expires int64  `json:"expires"`
}

// decodeRecoveryRequest applies the same origin and JSON checks as password login.
func (server *Server) decodeRecoveryRequest(writer http.ResponseWriter, request *http.Request, target any) bool {
	writer.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", "POST")
		fail(writer, http.StatusMethodNotAllowed, "method not allowed")
		return false
	}
	if request.Header.Get("Origin") == "" || request.Header.Get("Origin") != strings.TrimRight(server.config.AppURL, "/") {
		fail(writer, http.StatusForbidden, "invalid origin")
		return false
	}
	contentType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		fail(writer, http.StatusUnsupportedMediaType, "expected application/json")
		return false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 16<<10)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		fail(writer, http.StatusBadRequest, "invalid request")
		return false
	}
	return true
}

// beginEmailVerification holds the WorkOS challenge in a short-lived encrypted cookie.
func (server *Server) beginEmailVerification(writer http.ResponseWriter, pendingToken string) {
	state, err := workos.Seal(verificationState{Token: pendingToken, Expires: time.Now().Add(10 * time.Minute).Unix()}, server.config.CookiePassword)
	if err != nil {
		fail(writer, http.StatusServiceUnavailable, "authentication unavailable")
		return
	}
	server.cookie(writer, verificationCookie, state, 600)
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(writer).Encode(map[string]string{"status": "email_verification_required"})
}

// VerifyEmail exchanges the email code and pending challenge for a session.
func (server *Server) VerifyEmail(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Code string `json:"code"`
	}
	if !server.decodeRecoveryRequest(writer, request, &input) {
		return
	}
	cookie, err := request.Cookie(verificationCookie)
	if err != nil {
		fail(writer, http.StatusUnauthorized, "verification expired; sign in again")
		return
	}
	state, err := workos.Unseal[verificationState](cookie.Value, server.config.CookiePassword)
	if err != nil || state.Token == "" || state.Expires <= time.Now().Unix() {
		server.cookie(writer, verificationCookie, "", -1)
		fail(writer, http.StatusUnauthorized, "verification expired; sign in again")
		return
	}
	code := strings.TrimSpace(input.Code)
	if len(code) == 0 || len(code) > 32 {
		fail(writer, http.StatusBadRequest, "invalid verification code")
		return
	}
	provider, ok := server.provider.(recoveryProvider)
	if !ok {
		fail(writer, http.StatusServiceUnavailable, "authentication unavailable")
		return
	}
	response, err := provider.VerifyEmail(request.Context(), state.Token, code)
	if err != nil || response == nil || response.User == nil {
		// Keep the pending cookie so a user can retry an incorrect code or outage.
		if isRecoveryUnavailable(err) {
			fail(writer, http.StatusServiceUnavailable, "verification temporarily unavailable")
			return
		}
		fail(writer, http.StatusUnauthorized, "verification failed; check the code or sign in again")
		return
	}
	server.cookie(writer, verificationCookie, "", -1)
	server.createSession(writer, request, response, true)
}

// ForgotPassword requests a reset without revealing whether the account exists.
func (server *Server) ForgotPassword(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Email string `json:"email"`
	}
	if !server.decodeRecoveryRequest(writer, request, &input) {
		return
	}
	email := strings.TrimSpace(input.Email)
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email {
		fail(writer, http.StatusBadRequest, "invalid email")
		return
	}
	provider, ok := server.provider.(recoveryProvider)
	if !ok {
		fail(writer, http.StatusServiceUnavailable, "authentication unavailable")
		return
	}
	err = provider.RequestPasswordReset(request.Context(), email)
	if err != nil {
		// Do not expose WorkOS responses, which can contain reset tokens or user details.
		var apiError *workos.APIError
		if !errors.As(err, &apiError) || apiError.StatusCode >= 500 {
			fail(writer, http.StatusServiceUnavailable, "password reset temporarily unavailable")
			return
		}
	}
	writer.WriteHeader(http.StatusNoContent)
}

// ResetPassword consumes a WorkOS reset token and invalidates local sessions.
func (server *Server) ResetPassword(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Token       string `json:"token"`
		NewPassword string `json:"newPassword"`
	}
	if !server.decodeRecoveryRequest(writer, request, &input) {
		return
	}
	if input.Token == "" || len(input.Token) > 4096 || input.NewPassword == "" || len(input.NewPassword) > 1024 {
		fail(writer, http.StatusBadRequest, "invalid reset request")
		return
	}
	provider, ok := server.provider.(recoveryProvider)
	if !ok {
		fail(writer, http.StatusServiceUnavailable, "authentication unavailable")
		return
	}
	response, err := provider.ConfirmPasswordReset(request.Context(), input.Token, input.NewPassword)
	if err != nil || response == nil || response.User == nil || response.User.ID == "" {
		if isRecoveryUnavailable(err) {
			fail(writer, http.StatusServiceUnavailable, "password reset temporarily unavailable")
			return
		}
		fail(writer, http.StatusBadRequest, "password reset failed; check the link and password")
		return
	}
	// WorkOS revokes its sessions; remove local sessions so existing access tokens
	// cannot continue to authorize requests until their expiry.
	if _, err := server.database.ExecContext(request.Context(), `DELETE FROM auth_sessions WHERE workos_id=$1`, response.User.ID); err != nil {
		fail(writer, http.StatusServiceUnavailable, "password changed, but session cleanup failed; contact your administrator")
		return
	}
	server.cookie(writer, "session", "", -1)
	writer.WriteHeader(http.StatusNoContent)
}

// isRecoveryUnavailable distinguishes temporary WorkOS failures from invalid codes or tokens.
func isRecoveryUnavailable(err error) bool {
	if err == nil {
		return false
	}
	var networkError *workos.NetworkError
	if errors.As(err, &networkError) {
		return true
	}
	var apiError *workos.APIError
	return errors.As(err, &apiError) && (apiError.StatusCode == http.StatusRequestTimeout || apiError.StatusCode == http.StatusTooManyRequests || apiError.StatusCode >= 500)
}
