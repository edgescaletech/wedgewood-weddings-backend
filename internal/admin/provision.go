package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	workos "github.com/workos/workos-go/v7"
	"wedding-project/internal/authorization"
)

// IdentityProvider sends WorkOS emails and retrieves provider-owned invitation state.
type IdentityProvider interface {
	Send(context.Context, string, string) (*workos.UserInvite, error)
	Get(context.Context, string) (*workos.UserInvite, error)
	Resend(context.Context, string, string) (*workos.UserInvite, error)
}

type WorkOSIdentityProvider struct{ Client *workos.Client }

func (provider WorkOSIdentityProvider) Send(ctx context.Context, email, key string) (*workos.UserInvite, error) {
	return provider.Client.UserManagement().SendInvitation(ctx,
		&workos.UserManagementSendInvitationParams{Email: email}, workos.WithIdempotencyKey(key))
}

func (provider WorkOSIdentityProvider) Get(ctx context.Context, id string) (*workos.UserInvite, error) {
	return provider.Client.UserManagement().GetInvitation(ctx, id)
}

func (provider WorkOSIdentityProvider) Resend(ctx context.Context, id, key string) (*workos.UserInvite, error) {
	return provider.Client.UserManagement().ResendInvitation(ctx, id,
		&workos.UserManagementResendInvitationParams{}, workos.WithIdempotencyKey(key))
}

type invitationRequest struct {
	Email       string      `json:"email"`
	DisplayName string      `json:"displayName"`
	Roles       []roleGrant `json:"roles"`
}

type invitationOperation struct {
	key, email, userID                      string
	priorID, mode, deliveredID, completedID sql.NullString
	rejectedStatus                          sql.NullInt64
	createdAt                               time.Time
}

var errInvitationUnavailable = errors.New("invitation service unavailable")

func (server *Server) createUser(writer http.ResponseWriter, request *http.Request) {
	var input invitationRequest
	if !decodeMutation(writer, request, &input) {
		return
	}
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	address, err := mail.ParseAddress(input.Email)
	if err != nil || address.Address != input.Email || len(input.Email) > 254 ||
		input.DisplayName == "" || len(input.DisplayName) > 200 || len(input.Roles) > 50 {
		mutationError(writer, errInvalid)
		return
	}
	server.invite(writer, request, "", input)
}

func (server *Server) resendInvitation(writer http.ResponseWriter, request *http.Request) {
	userID, err := canonicalID(request.PathValue("id"))
	if err != nil {
		mutationError(writer, err)
		return
	}
	server.invite(writer, request, userID, invitationRequest{})
}

// invite reserves the request and validates it before contacting WorkOS. Only the
// local transactions take the Admin lock, and finalization reauthorizes the actor.
func (server *Server) invite(writer http.ResponseWriter, request *http.Request, userID string, input invitationRequest) {
	if !server.validOrigin(request) {
		respond(writer, http.StatusForbidden, map[string]string{"error": "invalid origin"})
		return
	}
	key, err := canonicalID(request.Header.Get("Idempotency-Key"))
	if err != nil {
		respond(writer, http.StatusBadRequest, map[string]string{"error": "Idempotency-Key must be a UUID"})
		return
	}
	payload, err := json.Marshal(input)
	if err != nil {
		mutationError(writer, err)
		return
	}
	ctx := request.Context()
	actor, _ := authenticatedUserID(ctx)
	operation := invitationOperation{key: key, email: input.Email, userID: userID}
	err = server.runMutation(ctx, actor, "users.manage", func(ctx context.Context, tx *sql.Tx, actor string) error {
		found, err := operation.read(ctx, tx, payload)
		if err != nil {
			return err
		}
		if found {
			if operation.completedID.Valid || operation.rejectedStatus.Valid {
				return nil
			}
			if userID == "" {
				return validateInvitationGrants(ctx, tx, actor, input)
			}
			var eligible bool
			err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1
                AND archived_at IS NULL AND workos_id IS NULL AND workos_invitation_id=$2)`,
				userID, operation.priorID).Scan(&eligible)
			if err == nil && !eligible {
				return errProvisionConflict
			}
			return err
		}

		if userID != "" {
			err = tx.QueryRowContext(ctx, `SELECT email, workos_invitation_id FROM users
				WHERE id=$1 AND archived_at IS NULL AND workos_id IS NULL AND workos_invitation_id IS NOT NULL FOR UPDATE`,
				userID).Scan(&operation.email, &operation.priorID)
		} else {
			err = validateInvitationGrants(ctx, tx, actor, input)
		}
		if err != nil {
			return err
		}
		// A different operation may already own this email or be resending this user's invitation.
		result, err := tx.ExecContext(ctx, `INSERT INTO user_invitation_operations(id,email,request,resend_user_id,prior_invitation_id)
			VALUES($1,$2,$3::jsonb,NULLIF($4,'')::uuid,$5) ON CONFLICT DO NOTHING`,
			key, operation.email, string(payload), userID, operation.priorID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			return errProvisionConflict
		}
		_, err = operation.read(ctx, tx, payload)
		return err
	})
	if err != nil {
		mutationError(writer, err)
		return
	}
	if operation.rejectedStatus.Valid {
		writeInvitationRejection(writer, int(operation.rejectedStatus.Int64))
		return
	}
	if operation.completedID.Valid {
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	if !operation.deliveredID.Valid {
		if err := server.deliverInvitation(ctx, &operation); err != nil {
			var rejection *invitationRejection
			if errors.As(err, &rejection) {
				writeInvitationRejection(writer, rejection.status)
			} else {
				mutationError(writer, err)
			}
			return
		}
	}

	server.mutate(writer, request, "users.manage", func(ctx context.Context, tx *sql.Tx, actor string) error {
		var completed bool
		if err := tx.QueryRowContext(ctx, `SELECT invitation_id IS NOT NULL FROM user_invitation_operations
			WHERE id=$1 FOR UPDATE`, key).Scan(&completed); err != nil || completed {
			return err
		}
		targetID, action := userID, "user.invite.resend"
		if userID == "" {
			action = "user.invite"
			targetID = uuid.NewString()
			if err := validateInvitationGrants(ctx, tx, actor, input); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO users(id,email,display_name,workos_invitation_id)
				VALUES($1,$2,$3,$4)`, targetID, input.Email, input.DisplayName, operation.deliveredID.String); err != nil {
				return constraintError(err)
			}
			for _, grant := range input.Roles {
				if err := assignGrant(ctx, tx, actor, targetID, grant); err != nil {
					return err
				}
			}
		} else {
			result, err := tx.ExecContext(ctx, `UPDATE users SET workos_invitation_id=$1,updated_at=now()
				WHERE id=$2 AND workos_invitation_id=$3 AND workos_id IS NULL AND archived_at IS NULL`,
				operation.deliveredID.String, userID, operation.priorID.String)
			if err != nil {
				return err
			}
			count, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if count == 0 {
				return errProvisionConflict
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE user_invitation_operations SET invitation_id=delivered_id WHERE id=$1`, key); err != nil {
			return err
		}
		return audit(ctx, tx, actor, action, "user", targetID, nil, map[string]string{"invitationId": operation.deliveredID.String})
	})
}

func (operation *invitationOperation) read(ctx context.Context, tx *sql.Tx, payload []byte) (bool, error) {
	var matches bool
	err := tx.QueryRowContext(ctx, `SELECT email, prior_invitation_id, delivery_mode, delivered_id, invitation_id,
		rejected_status, created_at, (request=$2::jsonb AND resend_user_id IS NOT DISTINCT FROM NULLIF($3,'')::uuid)
		FROM user_invitation_operations WHERE id=$1`, operation.key, string(payload), operation.userID).
		Scan(&operation.email, &operation.priorID, &operation.mode, &operation.deliveredID, &operation.completedID,
			&operation.rejectedStatus, &operation.createdAt, &matches)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err == nil && !matches {
		return true, errProvisionConflict
	}
	return err == nil, err
}

// validateInvitationGrants checks the entire grant set before sending an email.
func validateInvitationGrants(ctx context.Context, tx *sql.Tx, actor string, input invitationRequest) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE lower(email)=$1)`, input.Email).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return errProvisionConflict
	}
	if len(input.Roles) == 0 {
		return nil
	}
	allowed, err := authorization.HasGlobalPermissionTx(ctx, tx, actor, "roles.assign")
	if err != nil {
		return err
	}
	if !allowed {
		return errForbidden
	}
	seen := make(map[string]bool)
	for _, grant := range input.Roles {
		roleID, err := canonicalID(grant.RoleID)
		if err != nil {
			return err
		}
		_, resource, err := validateGrant(ctx, tx, actor, roleID, grant.VenueID)
		if err != nil {
			return err
		}
		key := roleID
		if resource != nil {
			key += resource.id
		}
		if seen[key] {
			return errInvalid
		}
		seen[key] = true
	}
	return nil
}
