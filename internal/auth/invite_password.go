package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	workos "github.com/workos/workos-go/v7"
)

func (server *Server) inviteSession(ctx context.Context, request *http.Request) (pendingInvite, error) {
	var target pendingInvite
	hash, err := inviteSessionHash(request)
	if err != nil {
		return target, err
	}
	target.Provider = &workos.UserInvite{}
	var started bool
	err = server.database.QueryRowContext(ctx, `SELECT u.id,u.email,u.display_name,o.invitation_id,o.workos_id,o.password_started_at IS NOT NULL
		FROM invitation_onboarding o JOIN users u ON u.id=o.user_id AND u.workos_invitation_id=o.invitation_id
		WHERE o.session_hash=$1 AND o.expires_at>now()
		AND u.workos_id IS NULL AND NOT u.is_active AND u.archived_at IS NULL`, hash[:]).
		Scan(&target.LocalID, &target.Email, &target.DisplayName, &target.Provider.ID, &target.WorkOSID, &started)
	if err == nil && started {
		return target, errAcceptedInvite
	}
	return target, err
}

func inviteSessionHash(request *http.Request) ([32]byte, error) {
	cookie, err := request.Cookie(inviteSessionCookie)
	if err != nil || len(cookie.Value) != 43 {
		return [32]byte{}, sql.ErrNoRows
	}
	return sha256.Sum256([]byte(cookie.Value)), nil
}

func (server *Server) SetInvitePassword(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		InvitationID    string `json:"invitationId"`
		Password        string `json:"password"`
		ConfirmPassword string `json:"confirmPassword"`
	}
	if !server.decodeRecoveryRequest(writer, request, &input) {
		return
	}
	if input.Password == "" || len(input.Password) > 1024 || input.Password != input.ConfirmPassword {
		fail(writer, http.StatusBadRequest, "Enter matching passwords.")
		return
	}
	hash, err := inviteSessionHash(request)
	if err != nil {
		fail(writer, http.StatusUnauthorized, "Verification expired. Request a new code.")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 20*time.Second)
	defer cancel()

	target, err := server.inviteSession(ctx, request)
	switch {
	case errors.Is(err, errAcceptedInvite):
		fail(writer, http.StatusConflict, "Password setup has already started. If setup succeeded, sign in with the password you chose.")
		return
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		writeInviteError(writer, err)
		return
	case errors.Is(err, sql.ErrNoRows) || target.Provider.ID != input.InvitationID:
		fail(writer, http.StatusUnauthorized, "Verification expired or changed. Reopen the invitation or request a new code.")
		return
	}
	provider := server.provider.(inviteAcceptanceProvider)
	user := workos.User{ID: target.WorkOSID, Email: target.Email}
	invite, err := provider.GetInvitation(ctx, target.Provider.ID)
	if err != nil {
		writeInviteError(writer, err)
		return
	}
	if !acceptedInvitationMatches(invite, target.Provider.ID, &user) {
		writeInviteError(writer, errInvalidInvite)
		return
	}

	// Commit the reservation before WorkOS can receive the password. A second
	// request, or an ambiguous network/DB failure, must never issue another PUT.
	var account loginAccount
	err = server.database.QueryRowContext(ctx, `UPDATE invitation_onboarding o SET password_started_at=now()
		FROM users u WHERE o.session_hash=$1 AND o.invitation_id=$2
		AND o.user_id=$3 AND o.workos_id=$4 AND lower(u.email)=lower($5)
		AND o.expires_at>now() AND o.password_started_at IS NULL
		AND u.id=o.user_id AND u.workos_invitation_id=o.invitation_id
		AND u.workos_id IS NULL AND NOT u.is_active AND u.archived_at IS NULL
		AND NOT EXISTS(SELECT 1 FROM users WHERE workos_id=o.workos_id)
		RETURNING u.id,o.invitation_id`, hash[:], input.InvitationID, target.LocalID, user.ID, user.Email).
		Scan(&account.id, &account.invitationID)
	if errors.Is(err, sql.ErrNoRows) {
		_, sessionErr := server.inviteSession(ctx, request)
		if errors.Is(sessionErr, errAcceptedInvite) {
			fail(writer, http.StatusConflict, "Password setup has already started. If setup succeeded, sign in with the password you chose.")
		} else {
			fail(writer, http.StatusUnauthorized, "Verification expired or changed. Reopen the invitation or request a new code.")
		}
		return
	}
	if err != nil {
		writeInviteError(writer, err)
		return
	}

	updated, err := provider.SetInvitePassword(ctx, user.ID, input.Password)
	if workOSStatus(err, 400, 422, 429) && server.releasePasswordAttempt(ctx, hash) {
		if workOSStatus(err, 429) {
			inviteRateLimited(writer)
		} else {
			fail(writer, http.StatusUnprocessableEntity, "WorkOS rejected this password. Choose a stronger password and try again.")
		}
		return
	}
	if err != nil || updated == nil || updated.ID != user.ID || !strings.EqualFold(updated.Email, user.Email) {
		invitePasswordUnconfirmed(writer)
		return
	}
	tx, err := server.database.BeginTx(ctx, nil)
	if err != nil {
		invitePasswordUnconfirmed(writer)
		return
	}
	defer tx.Rollback()
	if err := linkLoginAccount(ctx, tx, account, &user); err != nil || tx.Commit() != nil {
		invitePasswordUnconfirmed(writer)
		return
	}
	server.cookie(writer, inviteSessionCookie, "", -1)
	server.cookie(writer, inviteTokenCookie, "", -1)
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) releasePasswordAttempt(ctx context.Context, hash [32]byte) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, err := server.database.ExecContext(ctx, `UPDATE invitation_onboarding SET password_started_at=NULL WHERE session_hash=$1`, hash[:])
	return err == nil
}

func invitePasswordUnconfirmed(writer http.ResponseWriter) {
	fail(writer, http.StatusServiceUnavailable, "Password setup could not be confirmed. Try signing in with the password you chose. Contact your administrator if sign-in fails.")
}
