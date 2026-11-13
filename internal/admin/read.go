package admin

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"wedding-project/internal/auth"
	"wedding-project/internal/authorization"
)

// Server provides authenticated and authorized administrative endpoints.
type Server struct {
	DB       *sql.DB
	Auth     *auth.Server
	Access   *authorization.Service
	AppURL   string
	Identity IdentityProvider
}

// Register mounts read-only admin endpoints behind authentication and RBAC.
func (server *Server) Register(mux *http.ServeMux) {
	register := func(pattern, permission string, handler http.HandlerFunc) {
		mux.Handle(pattern, server.Auth.Middleware(server.Access.HasPermissionMiddleware(permission, nil, handler)))
	}
	register("GET /api/admin/users", "users.view", server.users)
	register("GET /api/admin/users/{id}", "users.view", server.user)
	register("GET /api/admin/roles", "roles.view", server.roles)
	register("GET /api/admin/roles/{id}", "roles.view", server.role)
	register("GET /api/admin/roles/{id}/permissions", "roles.view", server.role)
	register("GET /api/admin/permissions", "roles.view", server.permissions)
	register("GET /api/admin/venues", "venues.manage_access", server.venues)
	register("GET /api/admin/venues/{id}", "venues.manage_access", server.venue)
	server.registerMutations(mux)
}

// pagination limits response size and rejects malformed paging parameters.
func pagination(request *http.Request) (int, int, error) {
	limit, offset := 50, 0
	var err error
	if value := request.URL.Query().Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 100 {
			return 0, 0, errors.New("invalid limit")
		}
	}
	if value := request.URL.Query().Get("offset"); value != "" {
		offset, err = strconv.Atoi(value)
		if err != nil || offset < 0 {
			return 0, 0, errors.New("invalid offset")
		}
	}
	return limit, offset, nil
}

func respond(writer http.ResponseWriter, status int, body any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(body)
}

// queryJSON returns database-built JSON without exposing database errors.
func (server *Server) queryJSON(writer http.ResponseWriter, request *http.Request, query string, args ...any) {
	var data []byte
	err := server.DB.QueryRowContext(request.Context(), query, args...).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && string(data) == "null") {
		respond(writer, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if err != nil {
		respond(writer, http.StatusServiceUnavailable, map[string]string{"error": "admin service unavailable"})
		return
	}
	respond(writer, http.StatusOK, json.RawMessage(data))
}

func (server *Server) users(writer http.ResponseWriter, request *http.Request) {
	limit, offset, err := pagination(request)
	if err != nil {
		respond(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	search := strings.TrimSpace(request.URL.Query().Get("search"))
	if len(search) > 255 {
		respond(writer, http.StatusBadRequest, map[string]string{"error": "search too long"})
		return
	}
	active := request.URL.Query().Get("active")
	if active != "" && active != "true" && active != "false" {
		respond(writer, http.StatusBadRequest, map[string]string{"error": "invalid active filter"})
		return
	}
	server.queryJSON(writer, request, `SELECT COALESCE(jsonb_agg(to_jsonb(t)),'[]'::jsonb) FROM
 (SELECT id,workos_id,email,display_name,is_active,archived_at,created_at,updated_at FROM users WHERE position(lower($1) in lower(email))>0 AND ($4='' OR is_active=($4='true')) ORDER BY email,id LIMIT $2 OFFSET $3) t`, search, limit, offset, active)
}

func (server *Server) user(writer http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		respond(writer, http.StatusBadRequest, map[string]string{"error": "invalid user id"})
		return
	}
	server.queryJSON(writer, request, `SELECT jsonb_build_object('id',u.id,'workosId',u.workos_id,'email',u.email,'displayName',u.display_name,'isActive',u.is_active,'archivedAt',u.archived_at,
 'roles',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',ur.id,'roleId',r.id,'name',r.name,'scope',r.scope,'venueId',ur.venue_id)) FROM user_roles ur JOIN roles r ON r.id=ur.role_id WHERE ur.user_id=u.id),'[]'::jsonb)) FROM users u WHERE u.id=$1`, id)
}

func (server *Server) roles(writer http.ResponseWriter, request *http.Request) {
	server.queryJSON(writer, request, `SELECT COALESCE(jsonb_agg(jsonb_build_object('id',r.id,'name',r.name,'description',r.description,'scope',r.scope,
 'permissions',COALESCE((SELECT jsonb_agg(p.key ORDER BY p.key) FROM role_permissions rp JOIN permissions p ON p.id=rp.permission_id WHERE rp.role_id=r.id),'[]'::jsonb)) ORDER BY r.name),'[]'::jsonb) FROM roles r`)
}

func (server *Server) role(writer http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		respond(writer, http.StatusBadRequest, map[string]string{"error": "invalid role id"})
		return
	}
	server.queryJSON(writer, request, `SELECT jsonb_build_object('id',r.id,'name',r.name,'description',r.description,'scope',r.scope,
 'permissions',COALESCE((SELECT jsonb_agg(p.key ORDER BY p.key) FROM role_permissions rp JOIN permissions p ON p.id=rp.permission_id WHERE rp.role_id=r.id),'[]'::jsonb)) FROM roles r WHERE r.id=$1`, id)
}

func (server *Server) permissions(writer http.ResponseWriter, request *http.Request) {
	server.queryJSON(writer, request, `SELECT COALESCE(jsonb_agg(to_jsonb(p) ORDER BY p.key),'[]'::jsonb) FROM permissions p`)
}

func (server *Server) venues(writer http.ResponseWriter, request *http.Request) {
	limit, offset, err := pagination(request)
	if err != nil {
		respond(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	server.queryJSON(writer, request, `SELECT COALESCE(jsonb_agg(to_jsonb(t)),'[]'::jsonb) FROM
 (SELECT id,name,property_name,is_active FROM venues ORDER BY name,id LIMIT $1 OFFSET $2) t`, limit, offset)
}

func (server *Server) venue(writer http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if id == "" || len(id) > 64 {
		respond(writer, http.StatusBadRequest, map[string]string{"error": "invalid venue id"})
		return
	}
	server.queryJSON(writer, request, `SELECT to_jsonb(t) FROM (SELECT id,name,property_name,is_active FROM venues WHERE id=$1) t`, id)
}
