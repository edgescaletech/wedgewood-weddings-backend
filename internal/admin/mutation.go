package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"wedding-project/internal/auth"
	"wedding-project/internal/authorization"
)

var (
	errForbidden         = errors.New("permission denied")
	errInvalid           = errors.New("invalid request")
	errNotFound          = errors.New("not found")
	errProvisionConflict = errors.New("invitation operation conflicts with account details or current state")
	errVenueRoleConflict = errors.New("user already has a role at this venue")
	errConflict          = errors.New("change would remove the last effective administrator")
)

// mutate serializes privileged changes, rechecks access, and commits audit with the change.
func (server *Server) mutate(writer http.ResponseWriter, request *http.Request, permission string, change func(context.Context, *sql.Tx, string) error) {
	if !server.validOrigin(request) {
		respond(writer, http.StatusForbidden, map[string]string{"error": "invalid origin"})
		return
	}
	actor, ok := auth.User(request.Context())
	if !ok {
		respond(writer, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return
	}
	if err := server.runMutation(request.Context(), actor.ID, permission, change); err != nil {
		mutationError(writer, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) runMutation(ctx context.Context, actorID, permission string, change func(context.Context, *sql.Tx, string) error) error {
	tx, err := server.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(87239651)`); err != nil {
		return err
	}
	allowed, err := authorization.HasGlobalPermissionTx(ctx, tx, actorID, permission)
	if err != nil {
		return err
	}
	if !allowed {
		return errForbidden
	}
	if err := change(ctx, tx, actorID); err != nil {
		return err
	}
	if err := ensureAdministrator(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// ensureAdministrator requires an active global user with every management permission used in V1.
func ensureAdministrator(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT u.id FROM users u JOIN user_roles ur ON ur.user_id=u.id JOIN roles r ON r.id=ur.role_id WHERE u.is_active AND r.assignment_type='global' AND ur.resource_id IS NULL`)
	if err != nil {
		return err
	}
	var candidates []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	permissions := []string{"users.view", "users.manage", "roles.view", "roles.assign", "venues.manage_access"}
	for _, id := range candidates {
		complete := true
		for _, key := range permissions {
			allowed, err := authorization.HasGlobalPermissionTx(ctx, tx, id, key)
			if err != nil {
				return err
			}
			if !allowed {
				complete = false
				break
			}
		}
		if complete {
			return nil
		}
	}
	return errConflict
}

// audit writes an audit record in the same transaction as the privileged change.
func audit(ctx context.Context, tx *sql.Tx, actor, action, targetType, targetID string, resource *resourceScope, metadata any) error {
	data, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	var resourceID, resourceType, externalID *string
	if resource != nil {
		resourceID, resourceType, externalID = &resource.id, &resource.resourceType, &resource.externalID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_logs
		(id,actor_user_id,action,target_type,target_id,resource_id,resource_type,resource_external_id,metadata)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb)`,
		uuid.NewString(), actor, action, targetType, targetID, resourceID, resourceType, externalID, string(data))
	return err
}

func mutationError(writer http.ResponseWriter, err error) {
	status, message := http.StatusServiceUnavailable, "admin service unavailable"
	switch {
	case errors.Is(err, errForbidden):
		status, message = http.StatusForbidden, errForbidden.Error()
	case errors.Is(err, errInvalid):
		status, message = http.StatusBadRequest, errInvalid.Error()
	case errors.Is(err, errNotFound), errors.Is(err, sql.ErrNoRows):
		status, message = http.StatusNotFound, "not found"
	case errors.Is(err, errConflict), errors.Is(err, errProvisionConflict), errors.Is(err, errVenueRoleConflict):
		status, message = http.StatusConflict, err.Error()
	}
	respond(writer, status, map[string]string{"error": message})
}

func decodeMutation(writer http.ResponseWriter, request *http.Request, target any) bool {
	contentType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		respond(writer, http.StatusUnsupportedMediaType, map[string]string{"error": "expected application/json"})
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		respond(writer, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return false
	}
	return true
}

// canonicalID validates an external UUID and returns the form used by the database.
func canonicalID(id string) (string, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return "", errInvalid
	}
	return parsed.String(), nil
}

// sameUser compares parsed identities, independent of the UUID's text casing.
func sameUser(actor, target string) bool {
	actorID, actorErr := uuid.Parse(actor)
	targetID, targetErr := uuid.Parse(target)
	return actorErr == nil && targetErr == nil && actorID == targetID
}

func validID(id string) bool { _, err := canonicalID(id); return err == nil }

func (server *Server) validOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	return origin != "" && origin == strings.TrimRight(server.AppURL, "/")
}
