package main

import (
	"context"
	"database/sql"
	"net/http"
	"os"

	workos "github.com/workos/workos-go/v7"

	"wedding-project/internal/adapters/salesforce"
	"wedding-project/internal/admin"
	"wedding-project/internal/auth"
	"wedding-project/internal/authorization"
	"wedding-project/internal/currentuser"
	"wedding-project/internal/jobs"
)

// registerRoutes wires all HTTP routes onto mux.
// Keep main() free of route details — add new routes here.
func registerRoutes(
	mux *http.ServeMux,
	db *sql.DB,
	sf *salesforce.Client,
	syncWorker *jobs.EventSyncWorker,
	authServer *auth.Server,
) {
	// --- Health & docs ---
	mux.HandleFunc("/healthz", healthzHandler)
	mux.HandleFunc("/docs", docsHandler)
	mux.HandleFunc("/openapi.yaml", openAPIHandler)

	// --- Events API ---
	mux.Handle("/api/events", authServer.Middleware(venueAccess(db, "schedules.view", false, eventsHandler(db, sf, syncWorker))))
	mux.Handle("/api/events/", authServer.Middleware(venueAccess(db, "schedules.view", false, eventByIDHandler(db, sf))))
	mux.Handle("/api/sync/events", authServer.Middleware(syncEventsHandler(syncWorker)))

	// --- Labor Goals API ---
	mux.Handle("/api/labor-goals/summary", authServer.Middleware(venueAccess(db, "labor_goals.view", true, laborGoalsSummaryHandler(db))))
	mux.Handle("/api/labor-goals/targets", authServer.Middleware(venueAccess(db, "labor_goals.view", true, laborGoalsTargetsHandler(db))))
	mux.Handle("/api/labor-goals/periods", authServer.Middleware(accountingPeriodsHandler(db)))
	access := &authorization.Service{DB: db}
	requireLaborManage := func(next http.Handler) http.Handler {
		return authServer.Middleware(access.HasPermissionMiddleware("labor_goals.manage", func(ctx context.Context) (string, bool) {
			user, ok := auth.User(ctx)
			return user.ID, ok
		}, nil, next))
	}
	mux.Handle("/api/admin/labor-goals/upload", requireLaborManage(laborGoalsUploadHandler(db)))
	mux.Handle("/api/admin/accounting-periods/upload", requireLaborManage(accountingPeriodsUploadHandler(db)))

	adminServer := &admin.Server{
		DB: db, Auth: authServer, Access: access, AppURL: os.Getenv("APP_URL"),
		Identity: admin.WorkOSIdentityProvider{Client: workos.NewClient(os.Getenv("WORKOS_API_KEY"))},
	}
	adminServer.Register(mux)
	currentuser.Register(mux, authServer, access)
	registerAuthRoutes(mux, authServer)
}

func registerAuthRoutes(mux *http.ServeMux, authServer *auth.Server) {
	mux.HandleFunc("/auth/login", authServer.Login)
	mux.HandleFunc("/auth/password", authServer.PasswordLogin)
	mux.HandleFunc("/auth/invite", authServer.InviteDetails)
	mux.HandleFunc("/auth/invite/send-code", authServer.SendInviteCode)
	mux.HandleFunc("/auth/invite/password", authServer.SetInvitePassword)
	mux.HandleFunc("/auth/invite/verify", authServer.VerifyInviteEmail)

	mux.HandleFunc("/auth/verify-email", authServer.VerifyEmail)
	mux.HandleFunc("/auth/forgot-password", authServer.ForgotPassword)
	mux.HandleFunc("/auth/reset-password", authServer.ResetPassword)
	mux.HandleFunc("/auth/callback", authServer.Callback)
	mux.HandleFunc("/auth/logout", authServer.Logout)
	mux.Handle("/auth/me", authServer.Middleware(http.HandlerFunc(authServer.Me)))
}
