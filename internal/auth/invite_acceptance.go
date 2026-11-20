package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	workos "github.com/workos/workos-go/v7"
)

const (
	inviteTokenCookie   = "invite_context"
	inviteSessionCookie = "invite_onboarding"
	inviteResendSeconds = 30
)

var inviteCodeFormat = regexp.MustCompile(`^[0-9]{6}$`)

var (
	errInvalidInvite  = errors.New("invalid invitation")
	errExpiredInvite  = errors.New("this invitation expired; ask your administrator to resend it")
	errRevokedInvite  = errors.New("this invitation was revoked; contact your administrator")
	errAcceptedInvite = errors.New("this invitation was already accepted; sign in or contact your administrator")
)

type inviteAcceptanceProvider interface {
	invitationReader
	FindInvite(context.Context, string) (*workos.UserInvite, error)
	SendInviteCode(context.Context, string, *string) error
	VerifyInviteCode(context.Context, string, string, *string) (*workos.AuthenticateResponse, error)
	SetInvitePassword(context.Context, string, string) (*workos.User, error)
}

type pendingInvite struct {
	LocalID               string
	WorkOSID              string
	Email                 string
	DisplayName           string
	Provider              *workos.UserInvite
	CodeRecentlyRequested bool
	RetryAfterSeconds     int
}

type inviteContext struct {
	Token   string
	Expires int64
}

func (server *Server) InviteDetails(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Token string `json:"token"`
	}
	if !server.decodeRecoveryRequest(writer, request, &input) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 15*time.Second)
	defer cancel()

	// An explicit link takes precedence over any session left by another invite.
	if input.Token == "" {
		target, err := server.inviteSession(ctx, request)
		switch {
		case err == nil:
			writeInviteProfile(writer, target, "password")
			return
		case errors.Is(err, errAcceptedInvite):
			writeInviteError(writer, err)
			return
		case !errors.Is(err, sql.ErrNoRows):
			writeInviteError(writer, err)
			return
		}
	}
	token := server.invitationToken(request, input.Token)
	target, err := server.pendingInvitation(ctx, token)
	if err != nil {
		writeInviteError(writer, err)
		return
	}
	if input.Token != "" {
		sealed, err := workos.Seal(inviteContext{Token: token, Expires: time.Now().Add(30 * time.Minute).Unix()}, server.config.CookiePassword)
		if err != nil {
			writeInviteError(writer, err)
			return
		}
		server.cookie(writer, inviteTokenCookie, sealed, 1800)
	}
	step := "details"
	if target.CodeRecentlyRequested {
		step = "verify"
	}
	if current, err := server.inviteSession(ctx, request); err == nil && current.LocalID == target.LocalID &&
		current.Provider.ID == target.Provider.ID {
		step = "password"
	} else if input.Token != "" && current.Provider != nil && current.Provider.ID != target.Provider.ID {
		server.cookie(writer, inviteSessionCookie, "", -1)
	}
	writeInviteProfile(writer, target, step)
}

func writeInviteProfile(writer http.ResponseWriter, target pendingInvite, step string) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"email": target.Email, "displayName": target.DisplayName, "step": step, "invitationId": target.Provider.ID,
		"retryAfterSeconds": target.RetryAfterSeconds,
	})
}

func (server *Server) invitationToken(request *http.Request, token string) string {
	if token != "" {
		return token
	}
	cookie, err := request.Cookie(inviteTokenCookie)
	if err != nil {
		return ""
	}
	state, err := workos.Unseal[inviteContext](cookie.Value, server.config.CookiePassword)
	if err != nil || state.Expires <= time.Now().Unix() {
		return ""
	}
	return state.Token
}

func (server *Server) SendInviteCode(writer http.ResponseWriter, request *http.Request) {
	var input struct{}
	if !server.decodeRecoveryRequest(writer, request, &input) {
		return
	}
	writer.Header().Set("Access-Control-Expose-Headers", "Retry-After")
	ctx, cancel := context.WithTimeout(request.Context(), 15*time.Second)
	defer cancel()
	token := server.invitationToken(request, "")
	target, err := server.pendingInvitation(ctx, token)
	if err != nil {
		writeInviteError(writer, err)
		return
	}
	if target.RetryAfterSeconds > 0 {
		inviteRateLimited(writer, target.RetryAfterSeconds)
		return
	}
	var requestedAt time.Time
	err = server.database.QueryRowContext(ctx, `INSERT INTO invitation_onboarding (invitation_id,user_id,code_requested_at)
		SELECT workos_invitation_id,id,now() FROM users WHERE id=$1 AND workos_invitation_id=$2
			AND workos_id IS NULL AND archived_at IS NULL AND NOT is_active
		ON CONFLICT (invitation_id) DO UPDATE SET code_requested_at=now()
		WHERE invitation_onboarding.password_started_at IS NULL
			AND invitation_onboarding.code_requested_at <= now()-($3 * interval '1 second')
		RETURNING code_requested_at`, target.LocalID, target.Provider.ID, inviteResendSeconds).Scan(&requestedAt)
	if errors.Is(err, sql.ErrNoRows) {
		inviteRateLimited(writer, inviteResendSeconds)
		return
	}
	if err != nil {
		writeInviteError(writer, err)
		return
	}
	provider := server.provider.(inviteAcceptanceProvider)
	writer.Header().Set("Retry-After", strconv.Itoa(inviteResendSeconds))
	err = provider.SendInviteCode(ctx, target.Email, inviteMagicToken(target, token))
	if workOSStatus(err, 429) {
		inviteRateLimited(writer, inviteResendSeconds)
		return
	}
	if err != nil {
		writeInviteError(writer, err)
		return
	}
	if _, err := server.database.ExecContext(ctx, `UPDATE invitation_onboarding SET code_sent_at=now()
		WHERE invitation_id=$1 AND user_id=$2 AND code_requested_at=$3`, target.Provider.ID, target.LocalID, requestedAt); err != nil {
		writeInviteError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func inviteRateLimited(writer http.ResponseWriter, seconds int) {
	writer.Header().Set("Retry-After", strconv.Itoa(seconds))
	fail(writer, http.StatusTooManyRequests, "Wait before requesting another verification code.")
}

// An accepted invitation is no longer redeemable. Recovery sends a new code to
// the same email and still requires WorkOS proof of the original acceptance.
func inviteMagicToken(target pendingInvite, token string) *string {
	if target.Provider.State == "accepted" {
		return nil
	}
	return &token
}

func (server *Server) VerifyInviteEmail(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Code string `json:"code"`
	}
	if !server.decodeRecoveryRequest(writer, request, &input) {
		return
	}
	if !inviteCodeFormat.MatchString(input.Code) {
		fail(writer, http.StatusBadRequest, "Enter the six-digit verification code.")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 20*time.Second)
	defer cancel()
	token := server.invitationToken(request, "")
	target, err := server.pendingInvitation(ctx, token)
	if err != nil {
		writeInviteError(writer, err)
		return
	}
	var sent bool
	err = server.database.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM invitation_onboarding
		WHERE invitation_id=$1 AND user_id=$2 AND password_started_at IS NULL
		AND code_sent_at > now()-interval '10 minutes')`, target.Provider.ID, target.LocalID).Scan(&sent)
	if err != nil {
		writeInviteError(writer, err)
		return
	}
	if !sent {
		fail(writer, http.StatusBadRequest, "Request a new verification code.")
		return
	}
	provider := server.provider.(inviteAcceptanceProvider)
	response, err := provider.VerifyInviteCode(ctx, target.Email, input.Code, inviteMagicToken(target, token))
	if workOSStatus(err, 429) {
		inviteRateLimited(writer, 60)
		return
	}
	if workOSStatus(err, 400, 401, 422) {
		fail(writer, http.StatusBadRequest, "The verification code is incorrect or expired. Try again or request a new code.")
		return
	}
	if err != nil {
		writeInviteError(writer, err)
		return
	}
	if response == nil || response.User == nil || response.User.ID == "" || response.Impersonator != nil ||
		!response.User.EmailVerified || !strings.EqualFold(response.User.Email, target.Email) {
		writeInviteError(writer, errInvalidInvite)
		return
	}
	claims, err := server.verify(ctx, response.AccessToken)
	if err != nil || claims == nil || claims.Subject != response.User.ID {
		writeInviteError(writer, errors.New("onboarding token verification failed"))
		return
	}
	accepted, err := provider.GetInvitation(ctx, target.Provider.ID)
	if err != nil {
		writeInviteError(writer, err)
		return
	}
	if !acceptedInvitationMatches(accepted, target.Provider.ID, response.User) {
		writeInviteError(writer, errInvalidInvite)
		return
	}
	eligible, err := server.inviteCreatedIdentity(ctx, provider, target, response.User)
	if err != nil {
		writeInviteError(writer, err)
		return
	}
	if !eligible {
		fail(writer, http.StatusConflict, "This invitation requires an account created for onboarding. Contact your administrator.")
		return
	}
	session, err := random()
	if err != nil {
		writeInviteError(writer, err)
		return
	}
	hash := sha256.Sum256([]byte(session))
	result, err := server.database.ExecContext(ctx, `UPDATE invitation_onboarding o
		SET workos_id=$1,session_hash=$2,expires_at=now()+interval '10 minutes'
		FROM users u WHERE o.invitation_id=$3 AND o.user_id=$4 AND u.id=o.user_id
		AND u.workos_invitation_id=o.invitation_id AND u.workos_id IS NULL AND u.archived_at IS NULL
		AND NOT u.is_active AND lower(u.email)=lower($5) AND o.password_started_at IS NULL
		AND (o.workos_id IS NULL OR o.workos_id=$1)
		AND NOT EXISTS(SELECT 1 FROM invitation_onboarding WHERE user_id=u.id AND password_started_at IS NOT NULL)
		AND NOT EXISTS(SELECT 1 FROM users WHERE workos_id=$1)`, response.User.ID, hash[:], target.Provider.ID, target.LocalID, target.Email)
	if err != nil {
		writeInviteError(writer, err)
		return
	}
	if count, _ := result.RowsAffected(); count != 1 {
		writeInviteError(writer, errInvalidInvite)
		return
	}
	server.cookie(writer, inviteSessionCookie, session, 600)
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) pendingInvitation(ctx context.Context, token string) (pendingInvite, error) {
	var target pendingInvite
	if token == "" || len(token) > 512 || strings.ContainsAny(token, " \t\r\n") {
		return target, errInvalidInvite
	}
	provider, ok := server.provider.(inviteAcceptanceProvider)
	if !ok {
		return target, errors.New("invitation provider unavailable")
	}
	invite, err := provider.FindInvite(ctx, token)
	if workOSStatus(err, 400, 404) {
		return target, errInvalidInvite
	}
	if err != nil {
		return target, err
	}
	if invite == nil || invite.ID == "" || invite.Email == "" || invite.OrganizationID != nil {
		return target, errInvalidInvite
	}
	var linked, started, codeDelivered bool
	err = server.database.QueryRowContext(ctx, `SELECT u.id,u.email,u.display_name,
		(u.workos_id IS NOT NULL OR u.is_active),o.password_started_at IS NOT NULL,o.code_sent_at IS NOT NULL,
		COALESCE(o.code_sent_at>now()-interval '10 minutes',false),
		GREATEST(0, CEIL(EXTRACT(EPOCH FROM o.code_requested_at-now()))+$2)::int
		FROM users u LEFT JOIN invitation_onboarding o ON o.invitation_id=u.workos_invitation_id AND o.user_id=u.id
		WHERE u.workos_invitation_id=$1 AND u.archived_at IS NULL`, invite.ID, inviteResendSeconds).
		Scan(&target.LocalID, &target.Email, &target.DisplayName, &linked, &started, &codeDelivered, &target.CodeRecentlyRequested, &target.RetryAfterSeconds)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !strings.EqualFold(target.Email, invite.Email)) {
		return target, errInvalidInvite
	}
	if err != nil {
		return target, err
	}
	target.Provider = invite
	switch invite.State {
	case "revoked":
		return target, errRevokedInvite
	case "expired":
		return target, errExpiredInvite
	case "accepted":
		if linked || started || !codeDelivered || invite.AcceptedUserID == nil {
			return target, errAcceptedInvite
		}
		// Redemption already succeeded. Recovery requires a fresh WorkOS code
		// and proof of the same accepted identity, even after the link expires.
		return target, nil
	case "pending":
		if linked || started {
			return target, errInvalidInvite
		}
	default:
		return target, errInvalidInvite
	}
	expires, err := time.Parse(time.RFC3339, invite.ExpiresAt)
	if err != nil {
		return target, err
	}
	if !expires.After(time.Now()) {
		return target, errExpiredInvite
	}
	return target, nil
}

// A user who predates the initial invitation is outside new-hire onboarding.
// A replacement invitation can still refer to the original passwordless user.
func (server *Server) inviteCreatedIdentity(ctx context.Context, provider invitationReader, target pendingInvite, user *workos.User) (bool, error) {
	created, err := time.Parse(time.RFC3339, user.CreatedAt)
	if err != nil {
		return false, err
	}
	issued, err := time.Parse(time.RFC3339, target.Provider.CreatedAt)
	if err != nil || !created.Before(issued) {
		return err == nil, err
	}
	var originalID string
	err = server.database.QueryRowContext(ctx, `SELECT metadata->>'invitationId' FROM audit_logs
		WHERE target_id=$1 AND action='user.invite' AND metadata ? 'invitationId' LIMIT 1`, target.LocalID).Scan(&originalID)
	if errors.Is(err, sql.ErrNoRows) || originalID == target.Provider.ID {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	original, err := provider.GetInvitation(ctx, originalID)
	if err != nil {
		return false, err
	}
	if original == nil || original.ID != originalID || !strings.EqualFold(original.Email, target.Email) {
		return false, nil
	}
	issued, err = time.Parse(time.RFC3339, original.CreatedAt)
	return err == nil && !created.Before(issued), err
}

func writeInviteError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errInvalidInvite):
		fail(writer, http.StatusNotFound, errInvalidInvite.Error())
	case errors.Is(err, errExpiredInvite), errors.Is(err, errRevokedInvite):
		fail(writer, http.StatusGone, err.Error())
	case errors.Is(err, errAcceptedInvite):
		fail(writer, http.StatusConflict, errAcceptedInvite.Error())
	default:
		fail(writer, http.StatusServiceUnavailable, "Invitation setup is temporarily unavailable. Try again.")
	}
}

func workOSStatus(err error, statuses ...int) bool {
	var apiError *workos.APIError
	if !errors.As(err, &apiError) {
		return false
	}
	for _, status := range statuses {
		if apiError.StatusCode == status {
			return true
		}
	}
	return false
}
