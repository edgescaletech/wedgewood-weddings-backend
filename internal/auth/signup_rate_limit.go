package auth

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	signupAttemptLimit = 30
	signupWindow       = 10 * time.Minute
)

// signupLimiter keeps one bounded window for this server process.
type signupLimiter struct {
	mutex        sync.Mutex
	windowStart  time.Time
	attemptCount int
}

// allow counts an attempt and reports how long to wait if the window is full.
func (limiter *signupLimiter) allow(now time.Time) (bool, time.Duration) {
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()

	if limiter.windowStart.IsZero() || now.Sub(limiter.windowStart) >= signupWindow {
		limiter.windowStart = now
		limiter.attemptCount = 0
	}
	if limiter.attemptCount >= signupAttemptLimit {
		return false, signupWindow - now.Sub(limiter.windowStart)
	}
	limiter.attemptCount++
	return true, 0
}

// SignupRateLimit limits signup attempts for the lifetime of this handler.
func (server *Server) SignupRateLimit(next http.Handler) http.Handler {
	limiter := &signupLimiter{}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			next.ServeHTTP(writer, request)
			return
		}

		allowed, retryAfter := limiter.allow(time.Now())
		if !allowed {
			seconds := int((retryAfter + time.Second - 1) / time.Second)
			writer.Header().Set("Cache-Control", "no-store")
			writer.Header().Set("Retry-After", strconv.Itoa(seconds))
			fail(writer, http.StatusTooManyRequests, "too many signup attempts; please try again later")
			return
		}
		next.ServeHTTP(writer, request)
	})
}
