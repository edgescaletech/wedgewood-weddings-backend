package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	workos "github.com/workos/workos-go/v7"
)

type loginAccount struct {
	id           string
	invitationID sql.NullString
}

type invitationReader interface {
	GetInvitation(context.Context, string) (*workos.UserInvite, error)
}

func (provider workOS) GetInvitation(ctx context.Context, id string) (*workos.UserInvite, error) {
	return provider.c.UserManagement().GetInvitation(ctx, id)
}

func (server *Server) loginAccount(ctx context.Context, response *workos.AuthenticateResponse) (loginAccount, error) {
	user := response.User
	var account loginAccount
	// The schema requires an invitation for every account. Returning users
	// authenticate with the WorkOS ID already linked during onboarding.
	err := server.database.QueryRowContext(ctx, `SELECT id FROM users WHERE workos_id=$1 AND workos_invitation_id IS NOT NULL`, user.ID).Scan(&account.id)
	if !errors.Is(err, sql.ErrNoRows) || !user.EmailVerified || response.AuthenticationMethod == nil ||
		*response.AuthenticationMethod != workos.AuthenticateResponseAuthenticationMethodPassword {
		return account, err
	}

	// Password sign-in can reconcile a password update with an ambiguous
	// outcome. Match the identity reserved before that update.
	err = server.database.QueryRowContext(ctx, `SELECT u.id,u.workos_invitation_id FROM users u
		JOIN invitation_onboarding o ON o.user_id=u.id AND o.invitation_id=u.workos_invitation_id
		WHERE u.workos_id IS NULL AND NOT u.is_active
		AND lower(u.email)=$1 AND o.workos_id=$2 AND o.password_started_at < now()-interval '30 seconds'`,
		strings.ToLower(user.Email), user.ID).Scan(&account.id, &account.invitationID)
	if err != nil {
		return account, err
	}
	reader, ok := server.provider.(invitationReader)
	if !ok {
		return account, errors.New("invitation lookup unavailable")
	}
	lookupContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	invitation, err := reader.GetInvitation(lookupContext, account.invitationID.String)
	if err != nil {
		return account, err
	}
	if !acceptedInvitationMatches(invitation, account.invitationID.String, user) {
		return account, sql.ErrNoRows
	}
	return account, nil
}

// Acceptance must come from WorkOS for this exact invitation and authenticated user.
func acceptedInvitationMatches(invitation *workos.UserInvite, id string, user *workos.User) bool {
	return invitation != nil && invitation.ID == id &&
		string(invitation.State) == "accepted" && invitation.AcceptedUserID != nil &&
		*invitation.AcceptedUserID == user.ID && strings.EqualFold(invitation.Email, user.Email)
}
