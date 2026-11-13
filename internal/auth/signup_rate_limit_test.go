package auth

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A full window blocks attempts and opens again after the configured duration.
func TestSignupLimiterResets(t *testing.T) {
	limiter := &signupLimiter{}
	now := time.Now()
	for attempt := 0; attempt < signupAttemptLimit; attempt++ {
		if allowed, _ := limiter.allow(now); !allowed {
			t.Fatalf("attempt %d unexpectedly blocked", attempt+1)
		}
	}
	if allowed, wait := limiter.allow(now); allowed || wait != signupWindow {
		t.Fatalf("full window: allowed=%t wait=%v", allowed, wait)
	}
	if allowed, _ := limiter.allow(now.Add(signupWindow)); !allowed {
		t.Fatal("attempt blocked after the window expired")
	}
}

// Concurrent attempts share the same limit without racing.
func TestSignupRateLimitConcurrent(t *testing.T) {
	server := &Server{}
	var reached atomic.Int32
	handler := server.SignupRateLimit(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		reached.Add(1)
		writer.WriteHeader(http.StatusNoContent)
	}))
	var workers sync.WaitGroup
	for attempt := 0; attempt < signupAttemptLimit*2; attempt++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/auth/signup", nil))
			if response.Code != http.StatusNoContent && (response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "") {
				t.Errorf("unexpected response: %d", response.Code)
			}
		}()
	}
	workers.Wait()
	if reached.Load() != signupAttemptLimit {
		t.Fatalf("handler received %d requests, want %d", reached.Load(), signupAttemptLimit)
	}
}
