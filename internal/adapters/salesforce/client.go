package salesforce

import (
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

func (c *Client) ensureToken() (string, error) {
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

	newToken, err := c.fetchClientCredentialsToken()
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

func (c *Client) fetchClientCredentialsToken() (string, error) {
	tokenURL := c.cfg.MyDomainURL + "/services/oauth2/token"
	body := url.Values{}
	body.Set("grant_type", "client_credentials")
	body.Set("client_id", c.cfg.ClientID)
	body.Set("client_secret", c.cfg.ClientSecret)

	resp, err := c.httpClient.PostForm(tokenURL, body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var tr tokenResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return "", fmt.Errorf("parse token response: %w", err)
	}
	if tr.Error != "" {
		return "", fmt.Errorf("%s: %s", tr.Error, tr.ErrorDesc)
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
	records, err := c.doQuery(soql)
	if err != nil && strings.Contains(err.Error(), "401 unauthorized") {
		// Token may have expired — invalidate and retry once
		c.invalidateToken()
		return c.doQuery(soql)
	}
	return records, err
}

func (c *Client) doQuery(soql string) ([]json.RawMessage, error) {
	token, err := c.ensureToken()
	if err != nil {
		return nil, err
	}

	endpoint := c.cfg.MyDomainURL + "/services/data/v62.0/query"
	req, err := http.NewRequest("GET", endpoint, nil)
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

		pageReq, err := http.NewRequest("GET", fullNextURL, nil)
		if err != nil {
			break
		}
		pageReq.Header.Set("Authorization", "Bearer "+token)
		pageReq.Header.Set("Accept", "application/json")

		pageResp, err := c.httpClient.Do(pageReq)
		if err != nil || pageResp.StatusCode != http.StatusOK {
			if pageResp != nil {
				pageResp.Body.Close()
			}
			break
		}

		var pageQR QueryResponse
		decodeErr := json.NewDecoder(pageResp.Body).Decode(&pageQR)
		pageResp.Body.Close()
		if decodeErr != nil {
			break
		}

		allRecords = append(allRecords, pageQR.Records...)
		if pageQR.Done {
			break
		}
		nextURL = pageQR.NextRecordsURL
	}

	return allRecords, nil
}
