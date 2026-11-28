package pubsub

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

var schemaCache sync.Map // map[schemaID]*goavro.Codec (in-memory schema cache)

const numRequested = int32(10)

// pubsubEndpoint and topicName are read from env at startup so they can be
// overridden per environment (e.g. sandbox vs production) without code changes.
// Defaults match the standard Salesforce Pub/Sub API values.
func pubsubEndpoint() string {
	if v := os.Getenv("SF_PUBSUB_ENDPOINT"); v != "" {
		return v
	}
	return "api.pubsub.salesforce.com:7443"
}

func pubsubTopic() string {
	if v := os.Getenv("SF_PUBSUB_TOPIC"); v != "" {
		return v
	}
	return "/data/ChangeEvents"
}

type sfOAuthResponse struct {
	AccessToken string `json:"access_token"`
	InstanceURL string `json:"instance_url"`
	ID          string `json:"id"`
}

func authenticate(ctx context.Context, domainURL, clientID, clientSecret string) (sfOAuthResponse, error) {
	var tokenResp sfOAuthResponse
	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, domainURL+"/services/oauth2/token", strings.NewReader(data.Encode()))
	if err != nil {
		return tokenResp, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return tokenResp, fmt.Errorf("oauth request error: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tokenResp, err
	}
	if resp.StatusCode != 200 {
		return tokenResp, fmt.Errorf("oauth failed (%d): %s", resp.StatusCode, string(body))
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return tokenResp, fmt.Errorf("oauth json parse error: %w", err)
	}
	return tokenResp, nil
}

func unwrapAvro(v any) any {
	if m, ok := v.(map[string]any); ok && len(m) == 1 {
		for _, val := range m {
			return val
		}
	}
	return v
}
