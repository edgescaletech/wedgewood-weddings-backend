package admin

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
)

func (server *Server) activate(writer http.ResponseWriter, request *http.Request) {
	server.setUserStatus(writer, request, true)
}
func (server *Server) deactivate(writer http.ResponseWriter, request *http.Request) {
	server.setUserStatus(writer, request, false)
}

// setUserStatus changes account status and invalidates local sessions atomically.
func (server *Server) setUserStatus(writer http.ResponseWriter, request *http.Request, active bool) {
	userID := request.PathValue("id")
	server.mutate(writer, request, "users.manage", func(ctx context.Context, tx *sql.Tx, actor string) error {
		if !validID(userID) {
			return errInvalid
		}
		if sameUser(actor, userID) {
			return errForbidden
		}
		if active {
			if err := checkRetainedGrants(ctx, tx, actor, userID); err != nil {
				return err
			}
		} else if err := requireEditableUser(ctx, tx, userID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE users SET is_active=$1,updated_at=now() WHERE id=$2 AND workos_id IS NOT NULL`, active, userID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			return errNotFound
		}
		if !active {
			if _, err := tx.ExecContext(ctx, `DELETE FROM auth_sessions WHERE workos_id=(SELECT workos_id FROM users WHERE id=$1)`, userID); err != nil {
				return err
			}
		}
		action := "user.deactivate"
		if active {
			action = "user.activate"
		}
		return audit(ctx, tx, actor, action, "user", userID, nil, map[string]any{"outcome": "success"})
	})
}

// updateUser changes only the local display name; WorkOS owns identity details.
func (server *Server) updateUser(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		DisplayName string `json:"displayName"`
	}
	if !decodeMutation(writer, request, &input) {
		return
	}
	userID := request.PathValue("id")
	server.mutate(writer, request, "users.manage", func(ctx context.Context, tx *sql.Tx, actor string) error {
		name := strings.TrimSpace(input.DisplayName)
		if !validID(userID) || name == "" || len(name) > 200 {
			return errInvalid
		}
		if err := requireEditableUser(ctx, tx, userID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE users SET display_name=$1,updated_at=now() WHERE id=$2`, name, userID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			return errNotFound
		}
		return audit(ctx, tx, actor, "user.update", "user", userID, nil, map[string]any{"fields": []string{"displayName"}, "outcome": "success"})
	})
}

// requireEditableUser locks the account while a mutation runs. Pending
// invitations can still receive their initial assignments before acceptance.
func requireEditableUser(ctx context.Context, tx *sql.Tx, userID string) error {
	var active, linked bool
	err := tx.QueryRowContext(ctx, `SELECT is_active, workos_id IS NOT NULL
		FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&active, &linked)
	if err != nil {
		return err
	}
	if linked && !active {
		return errDeactivatedUser
	}
	return nil
}
