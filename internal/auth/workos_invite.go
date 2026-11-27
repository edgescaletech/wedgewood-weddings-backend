package auth

import (
	"context"

	workos "github.com/workos/workos-go/v7"
)

func (provider workOS) FindInvite(ctx context.Context, token string) (*workos.UserInvite, error) {
	return provider.c.UserManagement().FindInvitationByToken(ctx, token)
}

func (provider workOS) SendInviteCode(ctx context.Context, email string, token *string) error {
	_, err := provider.c.UserManagement().CreateMagicAuth(ctx, &workos.UserManagementCreateMagicAuthParams{
		Email: email, InvitationToken: token,
	})
	return err
}

func (provider workOS) VerifyInviteCode(ctx context.Context, email, code string, token *string) (*workos.AuthenticateResponse, error) {
	return provider.c.UserManagement().AuthenticateWithMagicAuth(ctx, &workos.UserManagementAuthenticateWithMagicAuthParams{
		Email: email, Code: code, InvitationToken: token,
	})
}

func (provider workOS) SetInvitePassword(ctx context.Context, id, password string) (*workos.User, error) {
	return provider.c.UserManagement().Update(ctx, id, &workos.UserManagementUpdateParams{
		Password: workos.UserManagementPasswordPlaintext{Password: password},
	}, workos.WithRequestMaxRetries(0))
}
