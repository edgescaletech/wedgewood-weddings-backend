package main

import (
	"bufio"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"wedding-project/internal/adapters/salesforce"
	"wedding-project/internal/auth"
	"wedding-project/internal/database"
	"wedding-project/internal/jobs"
	"wedding-project/internal/models"
	"wedding-project/internal/pubsub"
	"wedding-project/internal/repositories"
)

//go:embed openapi.yaml
var openAPISpec []byte

// ---------------------------------------------------------------------------
// .env loader
// ---------------------------------------------------------------------------

func loadEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return // .env is optional (Render injects vars directly)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		if os.Getenv(key) == "" { // don't override real env vars
			os.Setenv(key, val)
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// weekBounds returns Monday 00:00 and Sunday 23:59 for the ISO week
// that contains `t`.  If the query param "week" (YYYY-MM-DD) is supplied
// it is parsed; otherwise the current week is used.
func weekBounds(weekParam string) (start, end time.Time, err error) {
	var anchor time.Time
	if weekParam == "" {
		anchor = time.Now()
	} else {
		anchor, err = time.Parse("2006-01-02", weekParam)
		if err != nil {
			return start, end, fmt.Errorf("invalid week param %q: use YYYY-MM-DD", weekParam)
		}
	}
	// Shift to Monday
	wd := int(anchor.Weekday())
	if wd == 0 {
		wd = 7 // Sunday → 7
	}
	monday := anchor.AddDate(0, 0, -(wd - 1))
	start = time.Date(monday.Year(), monday.Month(), monday.Day(), 0, 0, 0, 0, monday.Location())
	end = start.AddDate(0, 0, 6) // Sunday
	return start, end, nil
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func healthzHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func openAPIHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(openAPISpec)
}

func docsHandler(w http.ResponseWriter, r *http.Request) {
	const html = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>Wedgewood Weddings API Docs</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css" />
  <link rel="icon" type="image/png" href="https://unpkg.com/swagger-ui-dist@5/favicon-32x32.png" />
  <style>
    body { margin: 0; background: #fafafa; font-family: sans-serif; }
    .topbar { display: none !important; }
    .swagger-ui .info { margin: 25px 0; }
  </style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    window.onload = () => {
      window.ui = SwaggerUIBundle({
        url: '/openapi.yaml',
        dom_id: '#swagger-ui',
        deepLinking: true,
        presets: [
          SwaggerUIBundle.presets.apis,
          SwaggerUIBundle.SwaggerUIStandalonePreset
        ],
        layout: "BaseLayout"
      });
    };
  </script>
</body>
</html>`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(html))
}

func eventsHandler(db *sql.DB, sf *salesforce.Client, syncWorker *jobs.EventSyncWorker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		weekParam := r.URL.Query().Get("week")
		start, end, err := weekBounds(weekParam)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		cacheKey := start.Format("2006-01-02")
		venueIdParam := r.URL.Query().Get("venueId")
		allowed := eventVenueAccess(r)
		if allowed == nil || (venueIdParam != "" && !allowed[venueIdParam]) {
			writeError(w, http.StatusForbidden, "permission denied")
			return
		}
		if len(allowed) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{
				"weekStart": cacheKey,
				"weekEnd":   end.Format("2006-01-02"),
				"events":    []models.Event{},
				"source":    "database",
			})
			return
		}

		// 1. Try serving from PostgreSQL first (sub-5ms)
		if db != nil {
			dbEvents, err := repositories.GetEventsByWeek(r.Context(), db, start, end, venueIdParam)
			if err == nil && len(dbEvents) > 0 {
				dbEvents = filterEventsByVenue(allowed, dbEvents)
				log.Printf("[DB HIT] Served %d events for week %s (venueId=%q) in ~2ms", len(dbEvents), cacheKey, venueIdParam)
				resp := map[string]any{
					"weekStart": cacheKey,
					"weekEnd":   end.Format("2006-01-02"),
					"events":    dbEvents,
					"source":    "database",
				}
				writeJSON(w, http.StatusOK, resp)
				return
			}
		}

		// 2. Cold DB: Fetch using worker
		log.Printf("[DB COLD] Fetching week %s from Salesforce...", cacheKey)
		if syncWorker != nil {
			if err := syncWorker.SyncWeek(r.Context(), start, end); err != nil {
				log.Printf("ERROR SyncWeek: %v", err)
				writeError(w, http.StatusInternalServerError, "failed to sync events from Salesforce")
				return
			}
			// Read back from DB for guaranteed consistency
			dbEvents, err := repositories.GetEventsByWeek(r.Context(), db, start, end, venueIdParam)
			if err == nil {
				dbEvents = filterEventsByVenue(allowed, dbEvents)
				writeJSON(w, http.StatusOK, map[string]any{
					"weekStart": cacheKey,
					"weekEnd":   end.Format("2006-01-02"),
					"events":    dbEvents,
					"source":    "database",
				})
				return
			}
		}

		// Direct Salesforce fallback if DB is nil or readback failed
		events, err := salesforce.GetWeeklyEventsContext(r.Context(), sf, start, end)
		if err != nil {
			log.Printf("ERROR GetWeeklyEvents fallback: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to fetch events from Salesforce")
			return
		}

		outputEvents := make([]salesforce.Event, 0, len(events))
		for _, event := range events {
			if (venueIdParam == "" || event.VenueID == venueIdParam) && allowed[event.VenueID] {
				outputEvents = append(outputEvents, event)
			}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"weekStart": cacheKey,
			"weekEnd":   end.Format("2006-01-02"),
			"events":    outputEvents,
			"source":    "salesforce",
		})
	}
}

// syncEventsHandler triggers an immediate non-blocking background sync for a week.
// Responds with 202 Accepted in under 5ms.
func syncEventsHandler(syncWorker *jobs.EventSyncWorker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		weekParam := r.URL.Query().Get("week")
		start, end, err := weekBounds(weekParam)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		started := false
		if syncWorker != nil {
			started = syncWorker.SyncWeekAsync(start, end)
		}

		statusMsg := "sync started in background"
		if !started {
			statusMsg = "sync already in progress for this week"
		}

		writeJSON(w, http.StatusAccepted, map[string]any{
			"status":    "accepted",
			"message":   statusMsg,
			"weekStart": start.Format("2006-01-02"),
			"weekEnd":   end.Format("2006-01-02"),
		})
	}
}

var salesforceEventIDPattern = regexp.MustCompile(`^[A-Za-z0-9]{15}([A-Za-z0-9]{3})?$`)

// eventByIDHandler handles GET /api/events/{id}
// Returns a single fully-enriched event JSON object, loading from PostgreSQL (<1ms).
func eventByIDHandler(db *sql.DB, sf *salesforce.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/events/")
		id = strings.TrimSpace(id)
		if id == "" {
			writeError(w, http.StatusBadRequest, "event id is required")
			return
		}
		allowed := eventVenueAccess(r)
		if allowed == nil {
			writeError(w, http.StatusForbidden, "permission denied")
			return
		}
		if len(allowed) == 0 {
			writeError(w, http.StatusNotFound, "event not found")
			return
		}

		// 1. Try reading directly from DB
		if db != nil {
			evt, err := repositories.GetEventByID(r.Context(), db, id)
			if err == nil && evt != nil {
				if !allowed[evt.VenueID] {
					writeError(w, http.StatusNotFound, "event not found")
					return
				}
				log.Printf("[DB HIT] Single event %s served from database (<1ms)", id)
				writeJSON(w, http.StatusOK, evt)
				return
			}
		}

		// 2. Fallback to Salesforce
		if !salesforceEventIDPattern.MatchString(id) {
			writeError(w, http.StatusNotFound, "event not found")
			return
		}

		log.Printf("[DB MISS] Fetching single event %s from Salesforce...", id)

		var evt *salesforce.Event
		fetchAndCache := func(conn *sql.Conn) error {
			var err error
			evt, err = salesforce.GetEventByIDContext(r.Context(), sf, id)
			if err != nil {
				return err
			}
			if conn != nil && evt != nil && allowed[evt.VenueID] {
				if err = repositories.UpsertEvent(r.Context(), conn, evt); err != nil {
					log.Printf("Event cache write failed for %s: %v", id, err)
				}
			}
			return nil
		}
		var err error
		if db != nil {
			conn, checkoutErr := db.Conn(r.Context())
			if checkoutErr != nil {
				err = fetchAndCache(nil)
			} else {
				defer conn.Close()
				err = database.WithEventCacheGate(r.Context(), conn, false, func() error { return fetchAndCache(conn) })
			}
		} else {
			err = fetchAndCache(nil)
		}
		if err != nil {
			log.Printf("ERROR GetEventByID(%s): %v", id, err)
			writeError(w, http.StatusNotFound, "event not found")
			return
		}
		if evt == nil || !allowed[evt.VenueID] {
			writeError(w, http.StatusNotFound, "event not found")
			return
		}

		writeJSON(w, http.StatusOK, evt)
	}
}

func main() {
	// Load .env from the same directory as the binary (works locally)
	exe, _ := os.Executable()
	loadEnv(filepath.Join(filepath.Dir(exe), ".env"))
	// Also try the working directory (useful during `go run .`)
	loadEnv(".env")

	cfg := salesforce.LoadConfig()
	if cfg.MyDomainURL == "" {
		log.Fatal("SF_MY_DOMAIN_URL is required")
	}

	sf, err := salesforce.NewClient(cfg)
	if err != nil {
		log.Fatalf("salesforce client: %v", err)
	}
	log.Println("Salesforce client ready")

	db, err := database.Open(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal("database connection failed")
	}
	defer db.Close()
	pubsub.StartBackgroundSubscriber(context.Background(), db, sf, cfg.MyDomainURL, cfg.ClientID, cfg.ClientSecret)

	// Initialize background async sync worker and scheduled pre-warming
	var syncWorker *jobs.EventSyncWorker
	if db != nil && sf != nil {
		syncWorker = jobs.NewEventSyncWorker(db, sf)

		syncInterval := 1 * time.Hour
		intervalVal := os.Getenv("SF_TO_DB_SYNC_INTERVAL")
		if intervalVal == "" {
			intervalVal = os.Getenv("SYNC_INTERVAL")
		}
		if intervalVal != "" {
			if parsed, err := time.ParseDuration(intervalVal); err == nil && parsed > 0 {
				syncInterval = parsed
			} else {
				log.Printf("Invalid SF_TO_DB_SYNC_INTERVAL %q, defaulting to %s", intervalVal, syncInterval)
			}
		}

		syncWeeksAhead := 4
		weeksVal := os.Getenv("SF_TO_DB_SYNC_WEEKS_AHEAD")
		if weeksVal == "" {
			weeksVal = os.Getenv("SYNC_WEEKS_AHEAD")
		}
		if weeksVal != "" {
			if parsed, err := strconv.Atoi(weeksVal); err == nil && parsed > 0 {
				syncWeeksAhead = parsed
			} else {
				log.Printf("Invalid SF_TO_DB_SYNC_WEEKS_AHEAD %q, defaulting to %d", weeksVal, syncWeeksAhead)
			}
		}

		syncWorker.StartScheduledSync(context.Background(), syncInterval, syncWeeksAhead)
	}

	authServer, err := auth.New(auth.Config{ClientID: os.Getenv("WORKOS_CLIENT_ID"), APIKey: os.Getenv("WORKOS_API_KEY"), RedirectURI: os.Getenv("WORKOS_REDIRECT_URI"), CookiePassword: os.Getenv("WORKOS_COOKIE_PASSWORD"), Issuer: os.Getenv("WORKOS_ISSUER"), AppURL: os.Getenv("APP_URL"), Secure: os.Getenv("COOKIE_SECURE") != "false"}, db)
	if err != nil {
		log.Fatalf("authentication configuration: %v", err)
	}

	mux := http.NewServeMux()
	registerRoutes(mux, db, sf, syncWorker, authServer)

	// Global CORS & logging middleware
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") == strings.TrimRight(os.Getenv("APP_URL"), "/") && r.Header.Get("Origin") != "" {
			w.Header().Set("Access-Control-Allow-Origin", r.Header.Get("Origin"))
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Add("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Idempotency-Key")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		mux.ServeHTTP(w, r)
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port
	log.Printf("Pure API server listening on %s (routes: /healthz, /api/events, /docs, /openapi.yaml)", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("server: %v", err)
	}
}
