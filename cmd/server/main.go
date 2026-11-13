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
	"strings"
	"time"

	"wedding-project/internal/adapters/salesforce"
	"wedding-project/internal/auth"
	"wedding-project/internal/database"
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

func eventsHandler(db *sql.DB, sf *salesforce.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		weekParam := r.URL.Query().Get("week")
		start, end, err := weekBounds(weekParam)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		cacheKey := start.Format("2006-01-02")
		venueIdParam := r.URL.Query().Get("venueId")
		bypassCache := r.URL.Query().Get("refresh") == "true"

		// 1. Try serving from PostgreSQL first (sub-5ms)
		if db != nil && !bypassCache {
			dbEvents, err := repositories.GetEventsByWeek(r.Context(), db, start, end, venueIdParam)
			if err == nil && len(dbEvents) > 0 {
				log.Printf("[DB HIT] Served %d events for week %s (venueId=%q) in ~2ms", len(dbEvents), cacheKey, venueIdParam)
				writeJSON(w, http.StatusOK, map[string]any{
					"weekStart": cacheKey,
					"weekEnd":   end.Format("2006-01-02"),
					"events":    dbEvents,
					"source":    "database",
				})
				return
			}
		}

		// 2. Fetch from Salesforce if not in DB or explicit refresh requested
		log.Printf("[DB MISS / REFRESH] Fetching week %s from Salesforce...", cacheKey)
		t0 := time.Now()

		events, err := salesforce.GetWeeklyEvents(sf, start, end)
		if err != nil {
			log.Printf("ERROR GetWeeklyEvents: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to fetch events from Salesforce")
			return
		}

		elapsed := time.Since(t0).Round(time.Millisecond)
		log.Printf("Salesforce query completed in %s — %d events for week %s", elapsed, len(events), cacheKey)

		// 3. Upsert into database so future requests are instant
		if db != nil && len(events) > 0 {
			go func(evts []salesforce.Event) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := repositories.UpsertEvents(ctx, db, evts); err != nil {
					log.Printf("ERROR background UpsertEvents: %v", err)
				} else {
					log.Printf("Successfully synchronized %d events into PostgreSQL", len(evts))
				}
			}(events)
		}

		outputEvents := events
		if venueIdParam != "" {
			filtered := make([]salesforce.Event, 0)
			for _, e := range events {
				if e.VenueID == venueIdParam {
					filtered = append(filtered, e)
				}
			}
			outputEvents = filtered
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"weekStart": cacheKey,
			"weekEnd":   end.Format("2006-01-02"),
			"events":    outputEvents,
			"source":    "salesforce",
		})
	}
}

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

		// 1. Try reading directly from DB
		if db != nil {
			evt, err := repositories.GetEventByID(r.Context(), db, id)
			if err == nil && evt != nil {
				log.Printf("[DB HIT] Single event %s served from database (<1ms)", id)
				writeJSON(w, http.StatusOK, evt)
				return
			}
		}

		// 2. Fallback to Salesforce
		log.Printf("[DB MISS] Fetching single event %s from Salesforce...", id)
		evt, err := salesforce.GetEventByID(sf, id)
		if err != nil {
			log.Printf("ERROR GetEventByID(%s): %v", id, err)
			writeError(w, http.StatusNotFound, fmt.Sprintf("event not found: %v", err))
			return
		}

		// Save to DB so future clicks are instant
		if db != nil && evt != nil {
			go func(e *salesforce.Event) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = repositories.UpsertEvent(ctx, db, e)
			}(evt)
		}

		writeJSON(w, http.StatusOK, evt)
	}
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

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
	authServer, err := auth.New(auth.Config{ClientID: os.Getenv("WORKOS_CLIENT_ID"), APIKey: os.Getenv("WORKOS_API_KEY"), RedirectURI: os.Getenv("WORKOS_REDIRECT_URI"), CookiePassword: os.Getenv("WORKOS_COOKIE_PASSWORD"), Issuer: os.Getenv("WORKOS_ISSUER"), AppURL: os.Getenv("APP_URL"), Secure: os.Getenv("COOKIE_SECURE") != "false"}, db)
	if err != nil {
		log.Fatalf("authentication configuration: %v", err)
	}

	mux := http.NewServeMux()

	// Pure API routes
	mux.HandleFunc("/healthz", healthzHandler)
	
	// mux.Handle("/api/events", authServer.Middleware(eventsHandler(db, sf)))
	// mux.Handle("/api/events/", authServer.Middleware(eventByIDHandler(db, sf)))
	mux.HandleFunc("/api/events", eventsHandler(db, sf))
	mux.HandleFunc("/api/events/", eventByIDHandler(db, sf))

	mux.HandleFunc("/auth/login", authServer.Login)
	mux.HandleFunc("/auth/password", authServer.PasswordLogin)
	mux.HandleFunc("/auth/verify-email", authServer.VerifyEmail)
	mux.HandleFunc("/auth/forgot-password", authServer.ForgotPassword)
	mux.HandleFunc("/auth/reset-password", authServer.ResetPassword)
	mux.HandleFunc("/auth/callback", authServer.Callback)
	mux.HandleFunc("/auth/logout", authServer.Logout)
	mux.Handle("/auth/me", authServer.Middleware(http.HandlerFunc(authServer.Me)))
	mux.Handle("/api/auth/me", authServer.Middleware(http.HandlerFunc(authServer.Me)))
	mux.HandleFunc("/docs", docsHandler)
	mux.HandleFunc("/openapi.yaml", openAPIHandler)

	// Global CORS & logging middleware
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") == strings.TrimRight(os.Getenv("APP_URL"), "/") && r.Header.Get("Origin") != "" {
			w.Header().Set("Access-Control-Allow-Origin", r.Header.Get("Origin"))
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Add("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
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
