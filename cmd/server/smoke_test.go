package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// TestAPISmoke checks the HTTP surface of an already-running API without credentials.
func TestAPISmoke(t *testing.T) {
	baseURL := os.Getenv("SMOKE_BASE_URL")
	if baseURL == "" {
		t.Skip("SMOKE_BASE_URL not configured; run make test-smoke against a running API")
	}
	base, err := url.Parse(baseURL)
	if err != nil || base.Hostname() == "" || (base.Scheme != "http" && base.Scheme != "https") ||
		base.User != nil || (base.Path != "" && base.Path != "/") || base.RawQuery != "" || base.Fragment != "" {
		t.Fatal("SMOKE_BASE_URL must be an HTTP(S) origin without credentials, a path, query, or fragment")
	}

	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	for _, check := range []struct {
		path   string
		status int
		key    string
		value  string
	}{
		{"/healthz", http.StatusOK, "status", "ok"},
		{"/auth/me", http.StatusUnauthorized, "error", "authentication required"},
		{"/auth/access", http.StatusUnauthorized, "error", "authentication required"},
		{"/api/me/venues", http.StatusUnauthorized, "error", "authentication required"},
		{"/api/events", http.StatusUnauthorized, "error", "authentication required"},
		{"/api/admin/users", http.StatusUnauthorized, "error", "authentication required"},
	} {
		t.Run(strings.TrimPrefix(check.path, "/"), func(t *testing.T) {
			endpoint := base.JoinPath(check.path)
			response, err := client.Get(endpoint.String())
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != check.status {
				t.Fatalf("GET %s returned %d, want %d", check.path, response.StatusCode, check.status)
			}
			var body map[string]string
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatalf("GET %s returned invalid JSON: %v", check.path, err)
			}
			if body[check.key] != check.value {
				t.Errorf("GET %s did not return the expected %s field", check.path, check.key)
			}
		})
	}
}
