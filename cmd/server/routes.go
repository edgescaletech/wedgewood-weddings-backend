package main

import (
	"database/sql"
	"net/http"

	"wedding-project/internal/adapters/salesforce"
	"wedding-project/internal/auth"
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
	mux.Handle("/api/events", authServer.Middleware(eventsHandler(db, sf, syncWorker)))
	mux.Handle("/api/events/", authServer.Middleware(eventByIDHandler(db, sf)))
	mux.Handle("/api/sync/events", authServer.Middleware(syncEventsHandler(syncWorker)))

	// --- Labor Goals API ---
	mux.Handle("/api/labor-goals/summary", authServer.Middleware(laborGoalsSummaryHandler(db)))
	mux.Handle("/api/labor-goals/targets", authServer.Middleware(laborGoalsTargetsHandler(db)))
	mux.Handle("/api/labor-goals/periods", authServer.Middleware(accountingPeriodsHandler(db)))
	mux.Handle("/api/admin/labor-goals/upload", authServer.Middleware(laborGoalsUploadHandler(db)))
	mux.Handle("/api/admin/accounting-periods/upload", authServer.Middleware(accountingPeriodsUploadHandler(db)))

	// --- Auth routes (Kaif's) ---
	mux.HandleFunc("/auth/login", authServer.Login)
	mux.HandleFunc("/auth/password", authServer.PasswordLogin)
	// Temporary signup endpoint; remove this route to disable account creation.
	mux.Handle("/auth/signup", authServer.SignupRateLimit(http.HandlerFunc(authServer.Signup)))
	mux.HandleFunc("/auth/verify-email", authServer.VerifyEmail)
	mux.HandleFunc("/auth/forgot-password", authServer.ForgotPassword)
	mux.HandleFunc("/auth/reset-password", authServer.ResetPassword)
	mux.HandleFunc("/auth/callback", authServer.Callback)
	mux.HandleFunc("/auth/logout", authServer.Logout)
	mux.Handle("/auth/me", authServer.Middleware(http.HandlerFunc(authServer.Me)))
}
