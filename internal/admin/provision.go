package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strings"

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
	resourceIDs                             []*string
}

var (
	errInvitationUnavailable  = errors.New("invitation service unavailable")
	errInvitationVenueChanged = errors.New("invitation venue no longer exists")
)

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
				if err := operation.validateGrants(ctx, tx, actor, input, true); permanentInvitationConflict(err) {
					return operation.rejectConflict(ctx, tx)
				} else {
					return err
				}
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
			err = operation.validateGrants(ctx, tx, actor, input, false)
		}
		if err != nil {
			return err
		}
		// A different operation may already own this email or be resending this user's invitation.
		resourceIDs, err := json.Marshal(operation.resourceIDs)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO user_invitation_operations(id,email,request,resend_user_id,prior_invitation_id,resource_ids)
            VALUES($1,$2,$3::jsonb,NULLIF($4,'')::uuid,$5,$6::jsonb) ON CONFLICT DO NOTHING`,
			key, operation.email, string(payload), userID, operation.priorID, string(resourceIDs))
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

	err = server.runMutation(ctx, actor, "users.manage", func(ctx context.Context, tx *sql.Tx, actor string) error {
		var completed bool
		if err := tx.QueryRowContext(ctx, `SELECT invitation_id IS NOT NULL,rejected_status FROM user_invitation_operations
			WHERE id=$1 FOR UPDATE NOWAIT`, key).Scan(&completed, &operation.rejectedStatus); err != nil || completed || operation.rejectedStatus.Valid {
			return err
		}
		targetID, action := userID, "user.invite.resend"
		if userID == "" {
			action = "user.invite"
			if err := operation.validateGrants(ctx, tx, actor, input, true); err != nil {
				if permanentInvitationConflict(err) {
					return operation.rejectConflict(ctx, tx)
				}
				return err
			}
			// A sign-in can take ownership after the eligibility check. Let the
			// unique index settle the race without aborting the audit transaction.
			err := tx.QueryRowContext(ctx, `INSERT INTO users(id,email,display_name,workos_invitation_id)
				VALUES($1,$2,$3,$4) ON CONFLICT (lower(email)) DO NOTHING RETURNING id`,
				uuid.NewString(), input.Email, input.DisplayName, operation.deliveredID.String).Scan(&targetID)
			if errors.Is(err, sql.ErrNoRows) {
				return operation.rejectConflict(ctx, tx)
			}
			if err != nil {
				return constraintError(err)
			}
			for index, grant := range input.Roles {
				if err := assignGrant(ctx, tx, actor, targetID, grant, operation.resourceID(index)); err != nil {
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
	if err != nil {
		mutationError(writer, err)
		return
	}
	if operation.rejectedStatus.Valid {
		writeInvitationRejection(writer, int(operation.rejectedStatus.Int64))
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusNoContent)
}

func permanentInvitationConflict(err error) bool {
	return errors.Is(err, errInvitationVenueChanged) || errors.Is(err, errProvisionConflict)
}

func (operation *invitationOperation) rejectConflict(ctx context.Context, tx *sql.Tx) error {
	// Release the reservation but retain the original operation and any WorkOS
	// delivery ID for reconciliation.
	return tx.QueryRowContext(ctx, `UPDATE user_invitation_operations
        SET rejected_status=COALESCE(rejected_status,409)
        WHERE id=$1 AND invitation_id IS NULL RETURNING rejected_status`, operation.key).
		Scan(&operation.rejectedStatus)
}

func (operation *invitationOperation) resourceID(index int) *string {
	if index >= len(operation.resourceIDs) {
		return nil
	}
	return operation.resourceIDs[index]
}

func (operation *invitationOperation) read(ctx context.Context, tx *sql.Tx, payload []byte) (bool, error) {
	// A delivery can hold this row across a provider call. Fail fast while
	// holding the shared Admin lock so unrelated mutations can proceed.
	var matches bool
	var resourceIDs []byte
	err := tx.QueryRowContext(ctx, `SELECT email, prior_invitation_id, delivery_mode, delivered_id, invitation_id,
		rejected_status, resource_ids, (request=$2::jsonb AND resend_user_id IS NOT DISTINCT FROM NULLIF($3,'')::uuid)
		FROM user_invitation_operations WHERE id=$1 FOR UPDATE NOWAIT`, operation.key, string(payload), operation.userID).
		Scan(&operation.email, &operation.priorID, &operation.mode, &operation.deliveredID, &operation.completedID,
			&operation.rejectedStatus, &resourceIDs, &matches)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err == nil && !matches {
		return true, errProvisionConflict
	}
	if err == nil && resourceIDs != nil {
		err = json.Unmarshal(resourceIDs, &operation.resourceIDs)
	}
	return err == nil, err
}

// validateGrants pins resources on creation and rechecks the same UUIDs on retries.
func (operation *invitationOperation) validateGrants(ctx context.Context, tx *sql.Tx, actor string, input invitationRequest, requireBinding bool) error {
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
	if !requireBinding {
		operation.resourceIDs = make([]*string, len(input.Roles))
	}
	for index, grant := range input.Roles {
		binding := operation.resourceID(index)
		if requireBinding && grant.VenueID != nil && binding == nil {
			return errInvitationVenueChanged
		}
		roleID, err := canonicalID(grant.RoleID)
		if err != nil {
			return err
		}
		_, resource, err := validateGrant(ctx, tx, actor, roleID, grant.VenueID, binding)
		if err != nil {
			return err
		}
		key := roleID
		if resource != nil {
			key += resource.id
			if !requireBinding {
				operation.resourceIDs[index] = &resource.id
			}
		}
		if seen[key] {
			return errInvalid
		}
		seen[key] = true
	}
	return nil
}
