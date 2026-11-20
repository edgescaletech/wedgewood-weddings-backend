package admin

import (
	"errors"
	"net/http"
	"strings"
)

type userFilters struct {
	search, active, status, role, venueID string
	publishOnly, directory                bool
}

// Opt into page metadata without changing the existing array responses.
func parseDirectoryFilters(request *http.Request) (search, active string, directory bool, err error) {
	query := request.URL.Query()
	search = strings.TrimSpace(query.Get("search"))
	active = query.Get("active")
	view := query.Get("view")
	switch {
	case len(search) > 255:
		err = errors.New("search too long")
	case active != "" && active != "true" && active != "false":
		err = errors.New("invalid active filter")
	case view != "" && view != "directory":
		err = errors.New("invalid view")
	}
	return search, active, view == "directory", err
}

func parseUserFilters(request *http.Request) (userFilters, error) {
	var filters userFilters
	var err error
	filters.search, filters.active, filters.directory, err = parseDirectoryFilters(request)
	if err != nil {
		return filters, err
	}
	query := request.URL.Query()
	filters.status = query.Get("status")
	filters.venueID = query.Get("venueId")
	filters.role = strings.TrimSpace(query.Get("role"))
	publish := query.Get("canPublish")
	switch filters.status {
	case "", "active", "invited", "inactive", "archived":
	default:
		return filters, errors.New("invalid status filter")
	}
	if len(filters.venueID) > 64 || len(filters.role) > 100 {
		return filters, errors.New("invalid role or venue filter")
	}
	if (publish != "" && publish != "true" && publish != "false") || (publish == "true" && filters.venueID == "") {
		return filters, errors.New("canPublish=true requires a venueId")
	}
	filters.publishOnly = publish == "true"
	return filters, nil
}

// Filter once before counting and paging. Aggregate the assignments only for
// users on the requested page. The registry UUID identifies the selected venue.
const userDirectoryQuery = `
WITH selected_venue AS (
    SELECT id, is_active FROM rbac_resources WHERE resource_type = 'venue' AND external_id = $7
), candidates AS (
    SELECT u.*,
           CASE WHEN u.archived_at IS NOT NULL THEN 'archived'
                WHEN u.is_active THEN 'active'
                WHEN u.workos_id IS NULL THEN 'invited'
                ELSE 'inactive' END AS account_status,
           u.is_active AND EXISTS (SELECT 1 FROM selected_venue WHERE is_active)
               AND EXISTS (
                   SELECT 1 FROM user_roles ur
                   JOIN role_permissions rp ON rp.role_id = ur.role_id
                   JOIN permissions p ON p.id = rp.permission_id
                   WHERE ur.user_id = u.id AND p.key = 'schedules.publish'
                     AND (ur.resource_id IS NULL OR ur.resource_id = (SELECT id FROM selected_venue))
               ) AS can_publish
    FROM users u
    WHERE (position(lower($1) in lower(u.email)) > 0 OR position(lower($1) in lower(u.display_name)) > 0)
      AND ($4 = '' OR u.is_active = ($4 = 'true'))
      AND (($6 = '' AND $7 = '') OR EXISTS (
          SELECT 1 FROM user_roles ur JOIN roles r ON r.id = ur.role_id
          WHERE ur.user_id = u.id AND ($6 = '' OR r.name = $6)
            AND ($7 = '' OR ur.resource_id = (SELECT id FROM selected_venue)
                 OR (EXISTS (SELECT 1 FROM selected_venue) AND ur.assignment_type = 'global'
                     AND ur.role_id = '00000000-0000-4000-8000-000000000001'::uuid))
      ))
), matches AS (
    SELECT * FROM candidates WHERE ($5 = '' OR account_status = $5) AND (NOT $8 OR can_publish)
), page AS (
    SELECT * FROM matches ORDER BY email, id LIMIT $2 OFFSET $3
), summaries AS (
    SELECT id, workos_id, email, display_name, is_active, archived_at, created_at, updated_at FROM page
), details AS (
    SELECT u.id, u.email,
           jsonb_build_object(
               'id', u.id, 'email', u.email, 'displayName', u.display_name, 'workosId', u.workos_id,
               'isActive', u.is_active, 'archivedAt', u.archived_at, 'canPublish', u.can_publish,
               'roles', COALESCE((
                   SELECT jsonb_agg(jsonb_build_object('id', ur.id, 'roleId', r.id, 'name', r.name,
                       'scope', r.assignment_type, 'venueId', rr.external_id) ORDER BY r.name, rr.external_id, ur.id)
                   FROM user_roles ur JOIN roles r ON r.id = ur.role_id
                   LEFT JOIN rbac_resources rr ON rr.id = ur.resource_id AND rr.resource_type = 'venue'
                   WHERE ur.user_id = u.id
               ), '[]'::jsonb)) AS item
    FROM page u
)
SELECT CASE WHEN $9 THEN jsonb_build_object(
    'items', COALESCE((SELECT jsonb_agg(item ORDER BY email, id) FROM details), '[]'::jsonb),
    'total', (SELECT count(*) FROM matches))
ELSE COALESCE((SELECT jsonb_agg(to_jsonb(s) ORDER BY email, id) FROM summaries s), '[]'::jsonb) END`

const venueDirectoryQuery = `
WITH available AS (
    SELECT id, name, property_name, is_active, NULLIF(btrim(region), '') AS region
    FROM venues WHERE $4 = '' OR is_active = ($4 = 'true')
), matches AS (
    SELECT * FROM available
    WHERE position(lower($3) in lower(name)) > 0 AND ($6 = '' OR region = $6)
), page AS (
    SELECT * FROM matches ORDER BY name, id LIMIT $1 OFFSET $2
), items AS (
    SELECT COALESCE(jsonb_agg(to_jsonb(p) ORDER BY name, id), '[]'::jsonb) AS data FROM page p
)
SELECT CASE WHEN $5 THEN jsonb_build_object(
    'items', data,
    'total', (SELECT count(*) FROM matches),
    'regions', COALESCE((SELECT jsonb_agg(DISTINCT region ORDER BY region)
                         FROM available WHERE region IS NOT NULL), '[]'::jsonb))
ELSE data END FROM items`
