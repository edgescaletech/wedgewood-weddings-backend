package salesforce

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Config holds Salesforce connection settings loaded from environment variables.
type Config struct {
	MyDomainURL  string // e.g. https://wedgewoodweddings.my.salesforce.com
	AccessToken  string // direct token (dev/demo shortcut)
	ClientID     string // OAuth client credentials
	ClientSecret string // OAuth client credentials
}

// LoadConfig reads SF_ env vars. Call after loading .env.
func LoadConfig() Config {
	return Config{
		MyDomainURL:  strings.TrimRight(os.Getenv("SF_MY_DOMAIN_URL"), "/"),
		AccessToken:  os.Getenv("SF_ACCESS_TOKEN"),
		ClientID:     os.Getenv("SF_CLIENT_ID"),
		ClientSecret: os.Getenv("SF_CLIENT_SECRET"),
	}
}

// Client is a resilient Salesforce REST API client with auto-refreshing OAuth tokens.
type Client struct {
	cfg        Config
	token      string
	tokenMu    sync.RWMutex
	httpClient *http.Client
}

// NewClient creates a Client. Token resolution happens lazily or on first call.
func NewClient(cfg Config) (*Client, error) {
	if cfg.MyDomainURL == "" {
		return nil, fmt.Errorf("SF_MY_DOMAIN_URL is required")
	}

	c := &Client{
		cfg:        cfg,
		token:      cfg.AccessToken,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}

	return c, nil
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	Error       string `json:"error"`
	ErrorDesc   string `json:"error_description"`
}

func (c *Client) ensureToken(ctx context.Context) (string, error) {
	c.tokenMu.RLock()
	tok := c.token
	c.tokenMu.RUnlock()

	if tok != "" {
		return tok, nil
	}

	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	// Double check after acquiring write lock
	if c.token != "" {
		return c.token, nil
	}

	if c.cfg.AccessToken != "" {
		c.token = c.cfg.AccessToken
		return c.token, nil
	}

	newToken, err := c.fetchClientCredentialsToken(ctx)
	if err != nil {
		return "", fmt.Errorf("salesforce oauth: %w", err)
	}
	c.token = newToken
	return c.token, nil
}

func (c *Client) invalidateToken() {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	c.token = ""
}

func (c *Client) fetchClientCredentialsToken(ctx context.Context) (string, error) {
	tokenURL := c.cfg.MyDomainURL + "/services/oauth2/token"
	body := url.Values{}
	body.Set("grant_type", "client_credentials")
	body.Set("client_id", c.cfg.ClientID)
	body.Set("client_secret", c.cfg.ClientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(body.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OAuth HTTP %d", resp.StatusCode)
	}

	var tr tokenResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return "", fmt.Errorf("parse token response: %w", err)
	}
	if tr.Error != "" {
		return "", fmt.Errorf("%s: %s", tr.Error, tr.ErrorDesc)
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("OAuth response missing access token")
	}
	return tr.AccessToken, nil
}

// QueryResponse is the envelope returned by the Salesforce SOQL REST endpoint.
type QueryResponse struct {
	TotalSize      int               `json:"totalSize"`
	Done           bool              `json:"done"`
	NextRecordsURL string            `json:"nextRecordsUrl"`
	Records        []json.RawMessage `json:"records"`
}

// Query executes a SOQL query with automatic token retrieval and retry on 401.
func (c *Client) Query(soql string) ([]json.RawMessage, error) {
	return c.QueryContext(context.Background(), soql)
}

// QueryContext executes a SOQL query with context cancellation, automatic token retrieval, and retry on 401.
func (c *Client) QueryContext(ctx context.Context, soql string) ([]json.RawMessage, error) {
	records, err := c.doQuery(ctx, soql)
	if err != nil && strings.Contains(err.Error(), "401 unauthorized") {
		// Token may have expired — invalidate and retry once
		c.invalidateToken()
		return c.doQuery(ctx, soql)
	}
	return records, err
}

// QueryAllContext includes soft-deleted records; never infer deletion from an absent row.
func (c *Client) QueryAllContext(ctx context.Context, soql string) ([]json.RawMessage, error) {
	rows, err := c.doQueryResource(ctx, soql, "queryAll")
	if err != nil && strings.Contains(err.Error(), "401 unauthorized") {
		c.invalidateToken()
		return c.doQueryResource(ctx, soql, "queryAll")
	}
	return rows, err
}

func (c *Client) doQuery(ctx context.Context, soql string) ([]json.RawMessage, error) {
	return c.doQueryResource(ctx, soql, "query")
}

func (c *Client) doQueryResource(ctx context.Context, soql, resource string) ([]json.RawMessage, error) {
	token, err := c.ensureToken(ctx)
	if err != nil {
		return nil, err
	}

	endpoint := c.cfg.MyDomainURL + "/services/data/v62.0/" + resource
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	q := req.URL.Query()
	q.Set("q", soql)
	req.URL.RawQuery = q.Encode()
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("salesforce: 401 unauthorized")
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("salesforce: %d %s", resp.StatusCode, string(raw))
	}

	var qr QueryResponse
	if err := json.NewDecoder(resp.Body).Decode(&qr); err != nil {
		return nil, fmt.Errorf("decode query response: %w", err)
	}

	allRecords := qr.Records

	// Auto-paginate through NextRecordsURL if more records exist
	nextURL := qr.NextRecordsURL
	for !qr.Done && nextURL != "" {
		fullNextURL := nextURL
		if !strings.HasPrefix(fullNextURL, "http") {
			fullNextURL = c.cfg.MyDomainURL + nextURL
		}

		base, baseErr := url.Parse(c.cfg.MyDomainURL)
		next, nextErr := url.Parse(fullNextURL)
		if baseErr != nil || nextErr != nil || base.Scheme != next.Scheme || base.Host != next.Host {
			return nil, fmt.Errorf("Salesforce pagination URL changed origin")
		}
		pageReq, err := http.NewRequestWithContext(ctx, "GET", fullNextURL, nil)
		if err != nil {
			return nil, fmt.Errorf("build query page request: %w", err)
		}
		pageReq.Header.Set("Authorization", "Bearer "+token)
		pageReq.Header.Set("Accept", "application/json")

		pageResp, err := c.httpClient.Do(pageReq)
		if err != nil {
			return nil, fmt.Errorf("Salesforce query page: %w", err)
		}
		if pageResp.StatusCode != http.StatusOK {
			pageResp.Body.Close()
			if pageResp.StatusCode == http.StatusUnauthorized {
				return nil, fmt.Errorf("salesforce: 401 unauthorized")
			}
			return nil, fmt.Errorf("incomplete Salesforce query page: HTTP %d", pageResp.StatusCode)
		}

		var pageQR QueryResponse
		decodeErr := json.NewDecoder(pageResp.Body).Decode(&pageQR)
		pageResp.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("decode Salesforce query page: %w", decodeErr)
		}

		allRecords = append(allRecords, pageQR.Records...)
		qr.Done = pageQR.Done
		if pageQR.Done {
			break
		}
		nextURL = pageQR.NextRecordsURL
	}

	if !qr.Done {
		return nil, fmt.Errorf("incomplete Salesforce query: no completion marker")
	}
	return allRecords, nil
}
