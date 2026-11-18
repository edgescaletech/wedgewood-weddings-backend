package admin

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	workos "github.com/workos/workos-go/v7"
)

type invitationRejection struct{ status int }

func (rejection *invitationRejection) Error() string { return "invitation rejected" }

func writeInvitationRejection(writer http.ResponseWriter, status int) {
	message := "WorkOS rejected the invitation details"
	if status == http.StatusConflict {
		message = "invitation can no longer be completed"
	}
	respond(writer, status, map[string]string{"error": message})
}

func (server *Server) deliverInvitation(ctx context.Context, operation *invitationOperation) error {
	if server.Identity == nil {
		return errInvitationUnavailable
	}
	deliveryContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if !operation.mode.Valid {
		mode := "send"
		if operation.priorID.Valid {
			prior, err := server.Identity.Get(deliveryContext, operation.priorID.String)
			if err != nil || prior == nil || prior.ID != operation.priorID.String || !strings.EqualFold(prior.Email, operation.email) {
				return errInvitationUnavailable
			}
			switch string(prior.State) {
			case "pending":
				mode = "resend"
			case "expired":
				// WorkOS resends only pending invitations. Replace an expired one.
			default:
				return errProvisionConflict
			}
		}
		if err := server.DB.QueryRowContext(deliveryContext, `UPDATE user_invitation_operations
			SET delivery_mode=COALESCE(delivery_mode,$2) WHERE id=$1 RETURNING delivery_mode`,
			operation.key, mode).Scan(&operation.mode); err != nil {
			return err
		}
	}

	// Lock only this operation while delivering. The shared Admin lock remains
	// available for assignments, account status changes, and actor revocation.
	tx, err := server.DB.BeginTx(deliveryContext, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var rejected sql.NullInt64
	if err := tx.QueryRowContext(deliveryContext, `SELECT delivered_id,rejected_status FROM user_invitation_operations
		WHERE id=$1 FOR UPDATE`, operation.key).Scan(&operation.deliveredID, &rejected); err != nil {
		return err
	}
	if operation.deliveredID.Valid {
		return tx.Commit()
	}
	if rejected.Valid {
		return &invitationRejection{status: int(rejected.Int64)}
	}

	// Old ambiguous operations require provider-side reconciliation before any
	// more writes. The application cannot assume WorkOS retains keys forever.
	if time.Since(operation.createdAt) > time.Hour {
		return errInvitationUnavailable
	}

	providerKey := "admin-invite-" + operation.key
	var invitation *workos.UserInvite
	if operation.mode.String == "resend" {
		invitation, err = server.Identity.Resend(deliveryContext, operation.priorID.String, providerKey)
	} else {
		invitation, err = server.Identity.Send(deliveryContext, operation.email, providerKey)
	}
	if err != nil {
		var apiError *workos.APIError
		if errors.As(err, &apiError) && (apiError.StatusCode == http.StatusBadRequest || apiError.StatusCode == http.StatusUnprocessableEntity) {
			if _, err := tx.ExecContext(deliveryContext, `UPDATE user_invitation_operations SET rejected_status=400 WHERE id=$1`, operation.key); err != nil {
				return err
			}
			if err := tx.Commit(); err != nil {
				return err
			}
			return &invitationRejection{status: http.StatusBadRequest}
		}
		return errInvitationUnavailable
	}
	if invitation == nil || invitation.ID == "" || !strings.EqualFold(invitation.Email, operation.email) ||
		(operation.mode.String == "resend" && invitation.ID != operation.priorID.String) {
		return errInvitationUnavailable
	}
	if _, err := tx.ExecContext(deliveryContext, `UPDATE user_invitation_operations SET delivered_id=$2 WHERE id=$1`,
		operation.key, invitation.ID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	operation.deliveredID = sql.NullString{String: invitation.ID, Valid: true}
	return nil
}
