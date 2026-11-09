package main

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"wedding-project/internal/adapters/salesforce"
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
// In-memory response cache (5-minute TTL per week key)
// Prevents repeated Salesforce API calls for the same week.
// ---------------------------------------------------------------------------

type cacheEntry struct {
	events    []salesforce.Event
	cachedAt  time.Time
}

type eventCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
	ttl     time.Duration
}

func newEventCache(ttl time.Duration) *eventCache {
	c := &eventCache{
		entries: make(map[string]cacheEntry),
		ttl:     ttl,
	}
	// Background goroutine purges stale entries every 10 minutes
	go func() {
		for range time.Tick(10 * time.Minute) {
			c.mu.Lock()
			for k, v := range c.entries {
				if time.Since(v.cachedAt) > c.ttl {
					delete(c.entries, k)
				}
			}
			c.mu.Unlock()
		}
	}()
	return c
}

func (c *eventCache) get(key string) ([]salesforce.Event, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[key]
	if !ok || time.Since(entry.cachedAt) > c.ttl {
		return nil, false
	}
	return entry.events, true
}

func (c *eventCache) set(key string, events []salesforce.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = cacheEntry{events: events, cachedAt: time.Now()}
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func healthzHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func authMeHandler(w http.ResponseWriter, r *http.Request) {
	// Demo role switch: passing header X-Role: admin simulates an admin user
	role := "gm"
	if r.Header.Get("X-Role") == "admin" || r.URL.Query().Get("role") == "admin" {
		role = "admin"
	}

	if role == "admin" {
		writeJSON(w, http.StatusOK, map[string]any{
			"userId":         "0051P00000AdminHQ",
			"name":           "Operations Director",
			"email":          "director@wedgewoodweddings.com",
			"role":           "admin",
			"assignedVenues": "ALL",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"userId": "0051P0000052FxxQAE",
		"name":   "Jensen Hand",
		"email":  "jensen.hand@wedgewoodweddings.com",
		"role":   "gm",
		"assignedVenues": []map[string]string{
			{"venueId": "0011P000012oMy9QAE", "venueName": "Ken Caryl Vista"},
		},
	})
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


func eventsHandler(sf *salesforce.Client, cache *eventCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		weekParam := r.URL.Query().Get("week")
		start, end, err := weekBounds(weekParam)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		cacheKey := start.Format("2006-01-02")
		bypassCache := r.URL.Query().Get("refresh") == "true"

		// Serve from cache if available and not explicitly bypassed
		if !bypassCache {
			if cached, ok := cache.get(cacheKey); ok {
				log.Printf("Cache HIT for week %s (%d events)", cacheKey, len(cached))
				outputEvents := cached
				if venueIdParam := r.URL.Query().Get("venueId"); venueIdParam != "" {
					filtered := make([]salesforce.Event, 0)
					for _, e := range cached {
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
					"fromCache": true,
				})
				return
			}
		}

		log.Printf("Cache MISS for week %s — querying Salesforce...", cacheKey)
		t0 := time.Now()

		events, err := salesforce.GetWeeklyEvents(sf, start, end)
		if err != nil {
			log.Printf("ERROR GetWeeklyEvents: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to fetch events from Salesforce")
			return
		}

		elapsed := time.Since(t0).Round(time.Millisecond)
		log.Printf("Salesforce query completed in %s — %d events for week %s", elapsed, len(events), cacheKey)

		// Store full nationwide list in cache
		cache.set(cacheKey, events)

		outputEvents := events
		if venueIdParam := r.URL.Query().Get("venueId"); venueIdParam != "" {
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
			"fromCache": false,
		})
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

	mux := http.NewServeMux()

	cache := newEventCache(5 * time.Minute)

	// Pure API routes
	mux.HandleFunc("/healthz", healthzHandler)
	mux.HandleFunc("/api/events", eventsHandler(sf, cache))
	mux.HandleFunc("/api/auth/me", authMeHandler)
	mux.HandleFunc("/docs", docsHandler)
	mux.HandleFunc("/openapi.yaml", openAPIHandler)

	// Global CORS & logging middleware
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
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
