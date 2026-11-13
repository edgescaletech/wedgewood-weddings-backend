package auth

import (
	"context"
	"database/sql"
	"errors"
	workos "github.com/workos/workos-go/v7"
	"net/http"
	"net/url"
	"time"
)

// Config holds the WorkOS, cookie and browser application settings.
type Config struct {
	ClientID       string
	APIKey         string
	RedirectURI    string
	CookiePassword string
	Issuer         string
	AppURL         string
	Secure         bool
}

// Server holds the authentication configuration, database and WorkOS client.
type Server struct {
	config   Config
	database *sql.DB
	provider provider
	verify   func(context.Context, string) (*claims, error)
}

// New validates configuration and prepares the authentication server.
func New(config Config, database *sql.DB) (*Server, error) {

	// A database stores local users and sessions. WorkOS credentials identify
	// this application, and the cookie password protects its encrypted state.
	if database == nil || config.ClientID == "" || config.APIKey == "" ||
		len(config.CookiePassword) < 32 || config.Issuer == "" {
		return nil, errors.New("missing authentication configuration")
	}

	// WorkOS sends the browser back to this address after sign-in. Allow HTTP
	// only for localhost development when secure cookies are disabled.
	redirectURL, err := url.Parse(config.RedirectURI)
	if err != nil || !isValidBrowserURL(redirectURL, config.Secure) {
		return nil, errors.New("invalid redirect URL")
	}

	// APP_URL is the application's origin: the callback redirects there and
	// logout checks the browser's Origin against it. Paths, queries and
	// fragments are not part of an origin and are therefore rejected.
	appURL, err := url.Parse(config.AppURL)
	if err != nil || !isValidBrowserURL(appURL, config.Secure) {
		return nil, errors.New("invalid app URL")
	}
	if (appURL.Path != "" && appURL.Path != "/") || appURL.RawQuery != "" || appURL.Fragment != "" {
		return nil, errors.New("invalid app URL")
	}

	// WorkOS publishes signing keys for each client ID. The verifier fetches
	// those keys and checks token signatures, issuer, expiry and identity.
	tokenVerifier := &verifier{
		url:      "https://api.workos.com/sso/jwks/" + url.PathEscape(config.ClientID),
		issuer:   config.Issuer,
		clientID: config.ClientID,
		client:   &http.Client{Timeout: 10 * time.Second},
	}

	// Keep the database, WorkOS SDK and verifier together for the handlers.
	return &Server{
		config:   config,
		database: database,
		provider: workOS{workos.NewClient(config.APIKey, workos.WithClientID(config.ClientID))},
		verify:   tokenVerifier.verify,
	}, nil
}

// isValidBrowserURL accepts HTTPS, or localhost HTTP for local development.
func isValidBrowserURL(parsedURL *url.URL, secureCookies bool) bool {
	// An address without a host cannot identify a browser destination.
	if parsedURL.Host == "" {
		return false
	}

	// HTTPS is accepted in any environment.
	if parsedURL.Scheme == "https" {
		return true
	}

	// Plain HTTP is only accepted for localhost with secure cookies disabled.
	isLocalhost := parsedURL.Hostname() == "localhost" || parsedURL.Hostname() == "127.0.0.1"
	return parsedURL.Scheme == "http" && !secureCookies && isLocalhost
}
