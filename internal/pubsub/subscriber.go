package pubsub

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	goavro "github.com/linkedin/goavro/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"

	"wedding-project/internal/adapters/salesforce"
	"wedding-project/internal/repositories"
	proto "wedding-project/proto"
)

const (
	pubsubEndpoint = "api.pubsub.salesforce.com:7443"
	topicName      = "/data/ChangeEvents"
	numRequested   = int32(10)
)

type sfOAuthResponse struct {
	AccessToken string `json:"access_token"`
	InstanceURL string `json:"instance_url"`
	ID          string `json:"id"`
}

func authenticate(domainURL, clientID, clientSecret string) (sfOAuthResponse, error) {
	var tokenResp sfOAuthResponse
	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)

	resp, err := http.PostForm(domainURL+"/services/oauth2/token", data)
	if err != nil {
		return tokenResp, fmt.Errorf("oauth request error: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return tokenResp, fmt.Errorf("oauth failed (%d): %s", resp.StatusCode, string(body))
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return tokenResp, fmt.Errorf("oauth json parse error: %w", err)
	}
	return tokenResp, nil
}

func getOrgID(instanceURL, accessToken string) (string, error) {
	req, _ := http.NewRequest("GET", instanceURL+"/services/oauth2/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("userinfo request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var userInfo map[string]any
	if err := json.Unmarshal(body, &userInfo); err != nil {
		return "", fmt.Errorf("userinfo json parse: %w", err)
	}

	if orgID, ok := userInfo["organization_id"].(string); ok {
		return orgID, nil
	}
	return "", fmt.Errorf("organization_id not found in userinfo")
}

func unwrapAvro(v any) any {
	if m, ok := v.(map[string]any); ok && len(m) == 1 {
		for _, val := range m {
			return val
		}
	}
	return v
}

// StartBackgroundSubscriber starts a persistent CDC listener in a background goroutine.
// It automatically reconciles changed Opportunities directly with PostgreSQL.
func StartBackgroundSubscriber(ctx context.Context, db *sql.DB, sf *salesforce.Client, domainURL, clientID, clientSecret string) {
	if domainURL == "" || clientID == "" || clientSecret == "" {
		log.Println("[PubSub CDC] Credentials missing, background subscriber skipped.")
		return
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				log.Println("[PubSub CDC] Subscriber shutting down.")
				return
			default:
				if err := runSubscriberSession(ctx, db, sf, domainURL, clientID, clientSecret); err != nil {
					log.Printf("[PubSub CDC] Session disconnected: %v. Reconnecting in 10s...", err)
					time.Sleep(10 * time.Second)
				}
			}
		}
	}()
}

func runSubscriberSession(ctx context.Context, db *sql.DB, sf *salesforce.Client, domainURL, clientID, clientSecret string) error {
	log.Println("[PubSub CDC] Authenticating with Salesforce...")
	tokenResp, err := authenticate(domainURL, clientID, clientSecret)
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	orgID, err := getOrgID(tokenResp.InstanceURL, tokenResp.AccessToken)
	if err != nil {
		return fmt.Errorf("orgId: %w", err)
	}

	tlsCreds := credentials.NewClientTLSFromCert(nil, "")
	conn, err := grpc.NewClient(pubsubEndpoint, grpc.WithTransportCredentials(tlsCreds))
	if err != nil {
		return fmt.Errorf("grpc connection: %w", err)
	}
	defer conn.Close()

	pubSubClient := proto.NewPubSubClient(conn)

	streamCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs(
		"accesstoken", tokenResp.AccessToken,
		"instanceurl", tokenResp.InstanceURL,
		"tenantid", orgID,
	))

	stream, err := pubSubClient.Subscribe(streamCtx)
	if err != nil {
		return fmt.Errorf("open subscribe stream: %w", err)
	}

	err = stream.Send(&proto.FetchRequest{
		TopicName:    topicName,
		ReplayPreset: proto.ReplayPreset_LATEST,
		NumRequested: numRequested,
	})
	if err != nil {
		return fmt.Errorf("send initial fetch: %w", err)
	}

	log.Printf("[PubSub CDC] Subscribed to %s. Listening for real-time changes...", topicName)

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		resp, err := stream.Recv()
		if err != nil {
			return fmt.Errorf("stream recv: %w", err)
		}

		events := resp.GetEvents()
		if len(events) == 0 {
			// Keepalive
			_ = stream.Send(&proto.FetchRequest{NumRequested: numRequested})
			continue
		}

		for _, event := range events {
			schemaID := event.GetEvent().GetSchemaId()
			payload := event.GetEvent().GetPayload()

			schemaResp, err := pubSubClient.GetSchema(streamCtx, &proto.SchemaRequest{SchemaId: schemaID})
			if err != nil {
				log.Printf("[PubSub CDC] Schema fetch error: %v", err)
				continue
			}

			codec, err := goavro.NewCodec(schemaResp.GetSchemaJson())
			if err != nil {
				log.Printf("[PubSub CDC] Avro codec error: %v", err)
				continue
			}

			native, _, err := codec.NativeFromBinary(payload)
			if err != nil {
				log.Printf("[PubSub CDC] Avro decode error: %v", err)
				continue
			}

			if eventMap, ok := native.(map[string]any); ok {
				handleCDCEvent(ctx, db, sf, eventMap)
			}
		}

		// Request more events
		_ = stream.Send(&proto.FetchRequest{NumRequested: numRequested})
	}
}

func handleCDCEvent(ctx context.Context, db *sql.DB, sf *salesforce.Client, eventMap map[string]any) {
	header, ok := eventMap["ChangeEventHeader"].(map[string]any)
	if !ok {
		return
	}

	entityName, _ := header["entityName"].(string)
	changeType := fmt.Sprintf("%v", unwrapAvro(header["changeType"]))

	recordIDs, _ := header["recordIds"].([]any)
	var firstRecordID string
	if len(recordIDs) > 0 {
		firstRecordID = fmt.Sprintf("%v", recordIDs[0])
	}

	log.Printf("[PubSub CDC] Received %s event on %s (ID: %s)", changeType, entityName, firstRecordID)

	// Audit trail in integration_inbox
	if err := repositories.LogPubSubEvent(ctx, db, entityName, changeType, firstRecordID, eventMap); err != nil {
		log.Printf("[PubSub CDC] Error logging to inbox: %v", err)
	}

	// If an Opportunity was created or updated, fetch enriched record and sync to PostgreSQL
	if entityName == "Opportunity" && firstRecordID != "" {
		if strings.EqualFold(changeType, "DELETE") {
			_, _ = db.ExecContext(ctx, "DELETE FROM events WHERE id = $1", firstRecordID)
			log.Printf("[PubSub CDC] Deleted Opportunity %s from PostgreSQL", firstRecordID)
			return
		}

		log.Printf("[PubSub CDC] Re-fetching Opportunity %s from Salesforce for DB sync...", firstRecordID)
		evt, err := salesforce.GetEventByID(sf, firstRecordID)
		if err != nil {
			log.Printf("[PubSub CDC] Failed to fetch updated Opportunity %s: %v", firstRecordID, err)
			return
		}

		if err := repositories.UpsertEvent(ctx, db, evt); err != nil {
			log.Printf("[PubSub CDC] Failed to upsert Opportunity %s to DB: %v", firstRecordID, err)
			return
		}

		log.Printf("[PubSub CDC] Successfully synchronized Opportunity %s (%s) to PostgreSQL!", evt.ID, evt.Name)
	}
}
