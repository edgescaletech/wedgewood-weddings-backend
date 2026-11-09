package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	goavro "github.com/linkedin/goavro/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"

	proto "wedding-project/proto"
)

// =====================================================================
// Salesforce Pub/Sub API Subscriber - wedding project
// Connects to Salesforce via gRPC (no ngrok, no public URL needed!)
// Subscribes to /data/ChangeEvents for real-time Opportunity updates.
// =====================================================================

const (
	pubsubEndpoint = "api.pubsub.salesforce.com:7443"
	topicName      = "/data/ChangeEvents" // Listens to ALL enabled objects
	numRequested   = int32(10)
)

// loadEnv reads KEY=VALUE pairs from path into the process environment.
// Real environment variables (e.g. set by Render) are never overwritten.
func loadEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return // .env is optional; Render injects vars directly
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
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}

// sfOAuthResponse is the OAuth2 token response from Salesforce
type sfOAuthResponse struct {
	AccessToken string `json:"access_token"`
	InstanceURL string `json:"instance_url"`
	ID          string `json:"id"` // contains org/user info URL
}

// authenticate obtains OAuth access token via Client Credentials flow
func authenticate(domainURL, clientID, clientSecret string) (sfOAuthResponse, error) {
	var tokenResp sfOAuthResponse

	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)

	resp, err := http.PostForm(domainURL+"/services/oauth2/token", data)
	if err != nil {
		return tokenResp, fmt.Errorf("OAuth request error: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return tokenResp, fmt.Errorf("OAuth failed (%d): %s", resp.StatusCode, string(body))
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return tokenResp, fmt.Errorf("OAuth JSON parse error: %w", err)
	}
	return tokenResp, nil
}

// getOrgID fetches the tenant/org ID from the OAuth identity URL
func getOrgID(instanceURL, accessToken string) (string, error) {
	// Call the userinfo endpoint to get the org ID
	req, _ := http.NewRequest("GET", instanceURL+"/services/oauth2/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("userinfo request error: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var userInfo map[string]any
	if err := json.Unmarshal(body, &userInfo); err != nil {
		return "", fmt.Errorf("userinfo JSON parse error: %w", err)
	}

	// org_id is in the userinfo response
	if orgID, ok := userInfo["organization_id"].(string); ok {
		return orgID, nil
	}
	return "", fmt.Errorf("organization_id not found in userinfo response")
}

// unwrapAvro unwraps Avro union types from goavro's map[string]interface{} format.
// goavro decodes union values as map[string]any{"double": 10800.0}
// This converts them to clean native Go values: 10800.0
func unwrapAvro(v any) any {
	if m, ok := v.(map[string]any); ok && len(m) == 1 {
		for _, val := range m {
			return val // return the inner value directly
		}
	}
	return v
}

// formatValue produces a clean string for an Avro-decoded value
func formatValue(key string, v any) string {
	v = unwrapAvro(v)
	switch key {
	case "LastModifiedDate", "CloseDate", "CreatedDate", "SystemModstamp":
		// Avro long fields for dates are Unix milliseconds
		if ms, ok := v.(int64); ok {
			t := time.UnixMilli(ms).Local()
			return t.Format("2006-01-02 15:04:05 MST")
		}
	case "Amount":
		if f, ok := v.(float64); ok {
			return fmt.Sprintf("₹%.2f", f)
		}
	}
	return fmt.Sprintf("%v", v)
}

// changedFieldNames extracts the names of actually-changed fields from the payload.
// In CDC, only modified fields have non-null values in the payload.
func changedFieldNames(eventMap map[string]any) []string {
	var fields []string
	for k, v := range eventMap {
		if k == "ChangeEventHeader" {
			continue
		}
		if v == nil {
			continue
		}
		fields = append(fields, k)
	}
	return fields
}

// printEvent formats and prints the wedding event payload cleanly
func printEvent(eventMap map[string]any) {
	fmt.Println()
	fmt.Println("==============================================================")
	fmt.Printf("🚨 SALESFORCE PUB/SUB EVENT RECEIVED! [%s]\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Println("==============================================================")

	// Extract ChangeEventHeader (CDC metadata)
	if header, ok := eventMap["ChangeEventHeader"].(map[string]any); ok {
		changeType := unwrapAvro(header["changeType"])
		fmt.Printf("📢 Change Type    : %v\n", changeType)

		if recIDs, ok := header["recordIds"].([]any); ok && len(recIDs) > 0 {
			fmt.Printf("🆔 Record ID      : %v\n", recIDs[0])
		}
		if entityName, ok := header["entityName"].(string); ok {
			fmt.Printf("📋 Entity         : %v\n", entityName)
		}

		// Show human-readable changed fields from the payload (non-null = changed)
		changed := changedFieldNames(eventMap)
		if len(changed) > 0 {
			fmt.Printf("✏️  Changed Fields  : %v\n", changed)
		}
	}

	fmt.Println("--------------------------------------------------------------")
	fmt.Println("📦 Changed Values:")

	// Print each non-null field in the event with clean formatting
	for k, v := range eventMap {
		if k == "ChangeEventHeader" {
			continue // already printed above
		}
		if v == nil {
			continue // skip null fields (unchanged fields are null in CDC)
		}
		// Only print fields that actually have values (= changed fields!)
		fmt.Printf("   %-30s : %s\n", k, formatValue(k, v))
	}

	fmt.Println("==============================================================")
}

func main() {
	fmt.Println("🔌 Salesforce Pub/Sub Subscriber (Change Data Capture)")
	fmt.Println("   Topic:", topicName)
	fmt.Println("   Connecting to:", pubsubEndpoint)
	fmt.Println()


	// Step 1: Load credentials (.env for local dev; Render injects real env vars)
	loadEnv(".env")

	domainURL := os.Getenv("SF_MY_DOMAIN_URL")
	clientID := os.Getenv("SF_CLIENT_ID")
	clientSecret := os.Getenv("SF_CLIENT_SECRET")

	if domainURL == "" || clientID == "" || clientSecret == "" {
		fmt.Println("❌ Missing SF_MY_DOMAIN_URL, SF_CLIENT_ID or SF_CLIENT_SECRET")
		os.Exit(1)
	}


	// Step 2: OAuth Authentication
	fmt.Println("🔐 Authenticating with Salesforce...")
	tokenResp, err := authenticate(domainURL, clientID, clientSecret)
	if err != nil {
		fmt.Println("❌ Authentication failed:", err)
		os.Exit(1)
	}
	fmt.Println("✅ Authentication successful!")
	fmt.Println("   Instance URL:", tokenResp.InstanceURL)

	// Step 3: Get Org/Tenant ID
	fmt.Println("🔍 Fetching Org ID...")
	orgID, err := getOrgID(tokenResp.InstanceURL, tokenResp.AccessToken)
	if err != nil {
		fmt.Println("❌ Failed to get Org ID:", err)
		os.Exit(1)
	}
	fmt.Println("✅ Org ID:", orgID)

	// Step 4: Connect to Salesforce Pub/Sub API via gRPC (TLS)
	fmt.Println("📡 Connecting to Salesforce Pub/Sub gRPC endpoint...")
	tlsCreds := credentials.NewClientTLSFromCert(nil, "")
	conn, err := grpc.NewClient(pubsubEndpoint, grpc.WithTransportCredentials(tlsCreds))
	if err != nil {
		fmt.Println("❌ gRPC connection failed:", err)
		os.Exit(1)
	}
	defer conn.Close()

	pubSubClient := proto.NewPubSubClient(conn)

	// Step 5: Attach auth metadata to context (required by Salesforce)
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(
		"accesstoken", tokenResp.AccessToken,
		"instanceurl", tokenResp.InstanceURL,
		"tenantid", orgID,
	))

	// Step 6: Open Subscribe stream
	fmt.Println("📬 Opening subscription stream to:", topicName)
	stream, err := pubSubClient.Subscribe(ctx)
	if err != nil {
		fmt.Println("❌ Subscribe stream error:", err)
		os.Exit(1)
	}

	// Step 7: Send initial FetchRequest (tell Salesforce we want events)
	err = stream.Send(&proto.FetchRequest{
		TopicName:    topicName,
		ReplayPreset: proto.ReplayPreset_LATEST, // start from NOW (latest events only)
		NumRequested: numRequested,
	})
	if err != nil {
		fmt.Println("❌ FetchRequest send error:", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("✅ CONNECTED! Listening for Opportunity changes in real-time...")
	fmt.Println("   (Go edit any Opportunity in Salesforce UI now!)")
	fmt.Println()

	// Step 8: Listen loop — receive events continuously
	for {
		resp, err := stream.Recv()
		if err != nil {
			fmt.Println("❌ Stream receive error:", err)
			break
		}

		events := resp.GetEvents()
		if len(events) == 0 {
			// Keep-alive message, request more events
			err = stream.Send(&proto.FetchRequest{
				NumRequested: numRequested,
			})
			if err != nil {
				fmt.Println("❌ Keep-alive FetchRequest error:", err)
				break
			}
			continue
		}

		// Process each event
		for _, event := range events {
			schemaID := event.GetEvent().GetSchemaId()
			payload := event.GetEvent().GetPayload()

			// Fetch Avro schema for this event's schemaId
			schemaResp, err := pubSubClient.GetSchema(ctx, &proto.SchemaRequest{SchemaId: schemaID})
			if err != nil {
				fmt.Println("⚠️  Could not fetch schema:", err)
				continue
			}

			schemaJSON := schemaResp.GetSchemaJson()

			// Decode the Avro binary payload using the schema
			codec, err := goavro.NewCodec(schemaJSON)
			if err != nil {
				fmt.Println("⚠️  Avro codec error:", err)
				continue
			}

			native, _, err := codec.NativeFromBinary(payload)
			if err != nil {
				fmt.Println("⚠️  Avro decode error:", err)
				continue
			}

			// Cast to map and print
			if eventMap, ok := native.(map[string]any); ok {
				printEvent(eventMap)
			}
		}

		// Request more events to keep the stream alive
		err = stream.Send(&proto.FetchRequest{
			NumRequested: numRequested,
		})
		if err != nil {
			fmt.Println("❌ FetchRequest error:", err)
			break
		}
	}

	fmt.Println("🔌 Stream closed. Exiting.")
}
