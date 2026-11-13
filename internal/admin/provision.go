package admin

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/mail"
	"strings"

	"github.com/google/uuid"
	workos "github.com/workos/workos-go/v7"
)

// IdentityProvider creates an identity without storing its password locally.
type IdentityProvider interface {
	Create(context.Context, string, string, string) (string, error)
}

// WorkOSIdentityProvider provisions an unverified identity through WorkOS.
type WorkOSIdentityProvider struct{ Client *workos.Client }

// Create sends the initial password directly to WorkOS and leaves email unverified.
func (provider WorkOSIdentityProvider) Create(ctx context.Context, email, name, password string) (string, error) {
	verified := false
	user, err := provider.Client.UserManagement().Create(ctx, &workos.UserManagementCreateParams{
		Email:         email,
		FirstName:     &name,
		EmailVerified: &verified,
		Password:      workos.UserManagementPasswordPlaintext{Password: password},
	})
	if err != nil || user == nil || user.ID == "" {
		return "", errors.New("identity provisioning failed")
	}
	return user.ID, nil
}

// createUser provisions a WorkOS identity and an inactive local account.
func (server *Server) createUser(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
		Password    string `json:"password"`
	}
	if !decodeMutation(writer, request, &input) {
		return
	}
	server.mutate(writer, request, "users.manage", func(ctx context.Context, tx *sql.Tx, actor string) error {
		email, name := strings.TrimSpace(input.Email), strings.TrimSpace(input.DisplayName)
		address, err := mail.ParseAddress(email)
		if err != nil || address.Address != email || len(email) > 254 || name == "" || len(name) > 200 || input.Password == "" || len(input.Password) > 1024 {
			return errInvalid
		}
		if server.Identity == nil {
			return errors.New("identity provider unavailable")
		}
		// A failed database commit can leave a WorkOS identity for an operator to reconcile.
		workosID, err := server.Identity.Create(ctx, email, name, input.Password)
		if err != nil {
			return err
		}
		userID := uuid.NewString()
		if _, err := tx.ExecContext(ctx, `INSERT INTO users(id,workos_id,email,is_active,display_name) VALUES($1,$2,$3,FALSE,$4)`, userID, workosID, email, name); err != nil {
			return err
		}
		return audit(ctx, tx, actor, "user.create", "user", userID, nil, map[string]any{"workosId": workosID, "outcome": "success"})
	})
}
