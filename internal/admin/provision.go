package admin

import (
	"context"
	"database/sql"
	"time"

	"errors"
	"net/http"
	"net/mail"
	"strings"

	"github.com/google/uuid"
	workos "github.com/workos/workos-go/v7"
)

// IdentityProvider creates or reconciles an identity using an application-issued operation ID.
type IdentityProvider interface {
	Create(context.Context, string, string, string, string) (string, error)
}

type WorkOSIdentityProvider struct{ Client *workos.Client }

var errIdentityProvisioning = errors.New("identity provisioning failed")

// Create uses the operation ID as both an external ID and an idempotency key.
// A retry resolves that exact external ID, even if the first response was lost.
func (provider WorkOSIdentityProvider) Create(ctx context.Context, operationID, email, name, password string) (string, error) {
	externalID := "admin-provision-" + operationID
	users := provider.Client.UserManagement()
	verify := func(user *workos.User) (string, error) {
		if user == nil || user.ID == "" || user.ExternalID == nil || *user.ExternalID != externalID ||
			!strings.EqualFold(user.Email, email) {
			return "", errIdentityProvisioning
		}
		return user.ID, nil
	}
	user, err := users.GetByExternalID(ctx, externalID)
	if err == nil {
		return verify(user)
	}
	var apiError *workos.APIError
	if !errors.As(err, &apiError) || apiError.StatusCode != http.StatusNotFound {
		return "", errIdentityProvisioning
	}

	verified := false
	user, err = users.Create(ctx, &workos.UserManagementCreateParams{
		ExternalID:    &externalID,
		Email:         email,
		FirstName:     &name,
		EmailVerified: &verified,
		Password:      workos.UserManagementPasswordPlaintext{Password: password},
	}, workos.WithIdempotencyKey(externalID))
	if err != nil {
		// The provider may have committed the create or another retry may have won.
		user, err = users.GetByExternalID(ctx, externalID)
		if err != nil {
			return "", errIdentityProvisioning
		}
	}
	return verify(user)
}

func (server *Server) createUser(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
		Password    string `json:"password"`
	}
	if !decodeMutation(writer, request, &input) {
		return
	}
	if !server.validOrigin(request) {
		respond(writer, http.StatusForbidden, map[string]string{"error": "invalid origin"})
		return
	}

	email, name := strings.TrimSpace(input.Email), strings.TrimSpace(input.DisplayName)
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || len(email) > 254 || name == "" || len(name) > 200 || input.Password == "" || len(input.Password) > 1024 {
		mutationError(writer, errInvalid)
		return
	}
	email = strings.ToLower(email)
	ctx := request.Context()
	actor, _ := authenticatedUserID(ctx) // The route has already authenticated the actor.
	allowed, err := server.Access.HasGlobalPermission(ctx, actor, "users.manage")
	if err != nil {
		mutationError(writer, err)
		return
	}
	if !allowed {
		mutationError(writer, errForbidden)
		return
	}

	// Commit the stable operation ID before making any external call.
	if _, err := server.DB.ExecContext(ctx, `INSERT INTO user_provisioning(id,email,display_name)
		VALUES($1,$2,$3) ON CONFLICT (email) DO NOTHING`, uuid.NewString(), email, name); err != nil {
		mutationError(writer, err)
		return
	}
	var operationID, recordedName string
	var completed sql.NullString
	if err := server.DB.QueryRowContext(ctx, `SELECT id,display_name,workos_id FROM user_provisioning
		WHERE email=$1`, email).Scan(&operationID, &recordedName, &completed); err != nil {
		mutationError(writer, err)
		return
	}
	if recordedName != name {
		mutationError(writer, errProvisionConflict)
		return
	}

	var workosID string
	if !completed.Valid {
		if server.Identity == nil {
			mutationError(writer, errIdentityProvisioning)
			return
		}
		providerContext, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		workosID, err = server.Identity.Create(providerContext, operationID, email, name, input.Password)
		if err != nil || workosID == "" {
			mutationError(writer, errIdentityProvisioning)
			return
		}
	}

	// mutate takes the shared lock only for the local writes and reauthorizes the actor.
	server.mutate(writer, request, "users.manage", func(ctx context.Context, tx *sql.Tx, actor string) error {
		var linked sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT workos_id FROM user_provisioning WHERE id=$1 FOR UPDATE`, operationID).Scan(&linked); err != nil {
			return err
		}
		if linked.Valid {
			if workosID != "" && linked.String != workosID {
				return errProvisionConflict
			}
			return nil
		}
		if workosID == "" {
			return errIdentityProvisioning
		}

		// A sign-in may already have linked this verified WorkOS ID. Preserve
		// the existing local ID and account status in that case.
		var userID string
		if err := tx.QueryRowContext(ctx, `INSERT INTO users(id,workos_id,email,is_active,display_name)
			VALUES($1,$2,$3,FALSE,$4)
			ON CONFLICT (workos_id) DO UPDATE SET workos_id=EXCLUDED.workos_id RETURNING id`,
			uuid.NewString(), workosID, email, name).Scan(&userID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE user_provisioning SET workos_id=$1 WHERE id=$2`, workosID, operationID); err != nil {
			return err
		}
		return audit(ctx, tx, actor, "user.create", "user", userID, nil, map[string]any{"workosId": workosID, "outcome": "success"})
	})
}
