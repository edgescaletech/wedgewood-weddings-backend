package auth

import (
	"context"
	workos "github.com/workos/workos-go/v7"
)

// provider isolates the WorkOS calls so they can be mocked in tests.
type provider interface {
	Start(string) (*workos.AuthKitPKCEAuthorizationURLResult, error)
	Exchange(context.Context, string, string) (*workos.AuthenticateResponse, error)
	Refresh(context.Context, string) (*workos.AuthenticateResponse, error)
	Revoke(context.Context, string) error
}

type workOS struct{ c *workos.Client }

// Start builds the hosted AuthKit login URL and PKCE verifier.
func (p workOS) Start(redirectURI string) (*workos.AuthKitPKCEAuthorizationURLResult, error) {
	// Select the hosted AuthKit screen; WorkOS generates the PKCE parameters.
	provider := "authkit"

	return p.c.GetAuthKitPKCEAuthorizationURL(
		workos.AuthKitAuthorizationURLParams{
			RedirectURI: redirectURI,
			Provider:    &provider,
		},
	)
}

// Exchange trades the authorization code and PKCE verifier for a WorkOS session.
func (p workOS) Exchange(ctx context.Context, code, verifier string) (*workos.AuthenticateResponse, error) {
	return p.c.AuthKitPKCECodeExchange(
		ctx,
		workos.AuthKitPKCECodeExchangeParams{
			Code:         code,
			CodeVerifier: verifier,
		},
	)
}

// Refresh exchanges a refresh token for a new pair of WorkOS tokens.
func (p workOS) Refresh(ctx context.Context, refreshToken string) (*workos.AuthenticateResponse, error) {
	return p.c.UserManagement().AuthenticateWithRefreshToken(
		ctx,
		&workos.UserManagementAuthenticateWithRefreshTokenParams{
			RefreshToken: refreshToken,
		},
	)
}

// Revoke ends a WorkOS session using its session ID.
func (p workOS) Revoke(ctx context.Context, sessionID string) error {
	return p.c.UserManagement().RevokeSession(
		ctx,
		&workos.UserManagementRevokeSessionParams{
			SessionID: sessionID,
		},
	)
}
