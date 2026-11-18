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

func (server *Server) loginAccount(ctx context.Context, user *workos.User) (loginAccount, error) {
	var account loginAccount
	// The schema requires an invitation for every account. Returning users
	// authenticate with the WorkOS ID already linked during onboarding.
	err := server.database.QueryRowContext(ctx, `SELECT id FROM users WHERE workos_id=$1 AND archived_at IS NULL AND workos_invitation_id IS NOT NULL`, user.ID).Scan(&account.id)
	if !errors.Is(err, sql.ErrNoRows) || !user.EmailVerified {
		return account, err
	}

	// Email locates a candidate only. WorkOS must attest acceptance by the
	// exact user ID in the cryptographically verified authentication response.
	err = server.database.QueryRowContext(ctx, `SELECT id,workos_invitation_id FROM users
		WHERE workos_id IS NULL AND archived_at IS NULL AND workos_invitation_id IS NOT NULL AND lower(email)=$1`,
		strings.ToLower(user.Email)).Scan(&account.id, &account.invitationID)
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
	if invitation == nil || invitation.ID != account.invitationID.String ||
		string(invitation.State) != "accepted" || invitation.AcceptedUserID == nil ||
		*invitation.AcceptedUserID != user.ID || !strings.EqualFold(invitation.Email, user.Email) {
		return account, sql.ErrNoRows
	}
	return account, nil
}
