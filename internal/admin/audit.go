package admin

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"
)

type auditEntry struct {
	ID                 string          `json:"id"`
	Action             string          `json:"action"`
	ActorName          string          `json:"actorName"`
	ActorEmail         string          `json:"actorEmail,omitempty"`
	TargetType         string          `json:"targetType,omitempty"`
	TargetID           string          `json:"targetId,omitempty"`
	TargetName         string          `json:"targetName,omitempty"`
	ResourceType       string          `json:"resourceType,omitempty"`
	ResourceExternalID string          `json:"resourceExternalId,omitempty"`
	Metadata           json.RawMessage `json:"metadata"`
	CreatedAt          time.Time       `json:"createdAt"`
}

// audit returns recent administrative activity to users who can manage accounts.
func (server *Server) audit(writer http.ResponseWriter, request *http.Request) {
	limit, offset, err := pagination(request)
	if err != nil {
		respond(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	var total int
	if err := server.DB.QueryRowContext(request.Context(), `SELECT count(*) FROM audit_logs`).Scan(&total); err != nil {
		respond(writer, http.StatusServiceUnavailable, map[string]string{"error": "admin service unavailable"})
		return
	}

	rows, err := server.DB.QueryContext(request.Context(), `
		SELECT a.id::text, a.action,
		       COALESCE(NULLIF(actor.display_name, ''), actor.email, 'Unavailable actor'),
		       COALESCE(actor.email, ''),
		       COALESCE(a.target_type, ''), COALESCE(a.target_id, ''),
		       COALESCE(NULLIF(target_user.display_name, ''), target_user.email,
		                target_role.name, target_venue.name, a.target_id, ''),
		       COALESCE(a.resource_type, ''), COALESCE(a.resource_external_id, ''),
		       a.metadata, a.created_at
		FROM audit_logs a
		LEFT JOIN users actor ON actor.id = a.actor_user_id
		LEFT JOIN users target_user
		       ON a.target_type = 'user' AND target_user.id::text = a.target_id
		LEFT JOIN roles target_role
		       ON a.target_type = 'role' AND target_role.id::text = a.target_id
		LEFT JOIN venues target_venue
		       ON a.target_type = 'venue' AND target_venue.id = a.target_id
		ORDER BY a.created_at DESC, a.id DESC
		LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		respond(writer, http.StatusServiceUnavailable, map[string]string{"error": "admin service unavailable"})
		return
	}
	defer rows.Close()

	items := make([]auditEntry, 0, limit)
	for rows.Next() {
		var entry auditEntry
		var actorEmail sql.NullString
		if err := rows.Scan(
			&entry.ID,
			&entry.Action,
			&entry.ActorName,
			&actorEmail,
			&entry.TargetType,
			&entry.TargetID,
			&entry.TargetName,
			&entry.ResourceType,
			&entry.ResourceExternalID,
			&entry.Metadata,
			&entry.CreatedAt,
		); err != nil {
			respond(writer, http.StatusServiceUnavailable, map[string]string{"error": "admin service unavailable"})
			return
		}
		if actorEmail.Valid {
			entry.ActorEmail = actorEmail.String
		}
		items = append(items, entry)
	}
	if err := rows.Err(); err != nil {
		respond(writer, http.StatusServiceUnavailable, map[string]string{"error": "admin service unavailable"})
		return
	}

	respond(writer, http.StatusOK, map[string]any{"items": items, "total": total})
}
