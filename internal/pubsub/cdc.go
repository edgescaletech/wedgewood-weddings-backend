package pubsub

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	goavro "github.com/linkedin/goavro/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	sf "wedding-project/internal/adapters/salesforce"
	"wedding-project/internal/database"
	"wedding-project/internal/models"
	"wedding-project/internal/repositories"
	proto "wedding-project/proto"
)

const cdcSource = "salesforce_pubsub"

var errRecoveryRequired = errors.New("CDC reconciliation required")

func cdcSubscriptionKey(org, topic string) string {
	return fmt.Sprintf("cdc:%x", sha256.Sum256([]byte(org+"\x00"+topic)))
}

func StartBackgroundSubscriber(ctx context.Context, db *sql.DB, client *sf.Client, domain, id, secret string) {
	if db == nil || client == nil || domain == "" || id == "" || secret == "" {
		log.Print("[PubSub CDC] Subscriber configuration incomplete")
		return
	}
	go func() {
		for ctx.Err() == nil {
			if err := runSubscriberSession(ctx, db, client, domain, id, secret); err != nil {
				log.Printf("[PubSub CDC] Session stopped: %v", err)
				if !waitCDC(ctx, 10*time.Second) {
					return
				}
			}
		}
	}()
}

func waitCDC(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func runSubscriberSession(parent context.Context, db *sql.DB, client *sf.Client, domain, id, secret string) error {
	conn, err := db.Conn(parent)
	if err != nil {
		return err
	}
	var leader bool
	if err = conn.QueryRowContext(parent, "SELECT pg_try_advisory_lock(hashtext('sf_pubsub_cdc_leader'))").Scan(&leader); err != nil {
		database.DiscardBadConn(conn)
		return err
	}
	if !leader {
		_ = conn.Close()
		waitCDC(parent, 15*time.Second)
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				probe, stop := context.WithTimeout(ctx, 3*time.Second)
				err := conn.PingContext(probe)
				stop()
				if err != nil {
					log.Printf("[PubSub CDC] Leader connection unhealthy: %v", err)
					cancel()
					return
				}
			}
		}
	}()
	defer func() {
		cancel()
		<-monitorDone
		database.ReleaseAdvisoryLock(conn, "SELECT pg_advisory_unlock(hashtext('sf_pubsub_cdc_leader'))", "")
	}()
	token, err := authenticate(ctx, domain, id, secret)
	if err != nil {
		return err
	}
	org, err := getOrgID(ctx, token.InstanceURL, token.AccessToken)
	if err != nil {
		return err
	}
	log.Printf("[PubSub CDC] Leader subscribing to %s", pubsubTopic())
	key := cdcSubscriptionKey(org, pubsubTopic())
	replay, recovery, err := loadCDCCheckpoint(ctx, conn, key)
	if err != nil {
		return err
	}
	rpc, err := grpc.NewClient(pubsubEndpoint(), grpc.WithTransportCredentials(credentials.NewClientTLSFromCert(nil, "")))
	if err != nil {
		return err
	}
	defer rpc.Close()
	service := proto.NewPubSubClient(rpc)
	streamCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("accesstoken", token.AccessToken, "instanceurl", token.InstanceURL, "tenantid", org))
	stream, err := service.Subscribe(streamCtx)
	if err != nil {
		return err
	}
	preset := proto.ReplayPreset_CUSTOM
	if recovery {
		preset = proto.ReplayPreset_LATEST
		replay = nil
	}
	if err = stream.Send(&proto.FetchRequest{TopicName: pubsubTopic(), ReplayPreset: preset, ReplayId: replay, NumRequested: numRequested}); err != nil {
		return err
	}
	return consumeCDCStream(streamCtx, conn, client, key, recovery, service, stream)
}

func consumeCDCStream(ctx context.Context, conn *sql.Conn, client *sf.Client, key string, recovery bool, service proto.PubSubClient, stream proto.PubSub_SubscribeClient) error {
	for {
		response, err := stream.Recv()
		if err != nil {
			if replayRejected(err, stream.Trailer()) {
				if stateErr := requireCDCRecovery(ctx, conn, key); stateErr != nil {
					return stateErr
				}
				return errRecoveryRequired
			}
			return err
		}
		// Receive the subscription anchor BEFORE loading the snapshot. Do not save
		// that anchor until reconciliation and every event in this response succeed.
		if recovery {
			log.Print("[PubSub CDC] Stream anchor received; reconciling cached scheduling scope before advancing replay")
			recoveryCtx, stop := context.WithTimeout(ctx, 30*time.Minute)
			err := reconcileCDCCache(recoveryCtx, conn, client)
			stop()
			if err != nil {
				return fmt.Errorf("CDC recovery failed; checkpoint unchanged: %w", err)
			}
			log.Print("[PubSub CDC] Reconciliation complete; applying buffered stream response")
			recovery = false
		}
		for _, event := range response.GetEvents() {
			payload, err := decodeCDCEvent(ctx, service, event)
			if err != nil {
				return err
			}
			if err = processCDCMessage(ctx, conn, client, key, event.GetReplayId(), payload); err != nil {
				if errors.Is(err, errRecoveryRequired) {
					if stateErr := requireCDCRecovery(ctx, conn, key); stateErr != nil {
						return stateErr
					}
				}
				return err
			}
		}
		// A keepalive cursor is safe only after all preceding events are committed.
		if cursor := response.GetLatestReplayId(); len(cursor) > 0 {
			if err = saveCDCCheckpoint(ctx, conn, key, cursor); err != nil {
				return err
			}
		}
		if response.GetPendingNumRequested() == 0 {
			if err = stream.Send(&proto.FetchRequest{NumRequested: numRequested}); err != nil {
				return err
			}
		}
	}
}

func replayRejected(err error, trailers metadata.MD) bool {
	const code = "sfdc.platform.eventbus.grpc.subscription.fetch.replayid.corrupted"
	if err != nil && strings.Contains(err.Error(), code) {
		return true
	}
	for _, value := range trailers.Get("error-code") {
		if value == code {
			return true
		}
	}
	return false
}

func loadCDCCheckpoint(ctx context.Context, conn *sql.Conn, key string) ([]byte, bool, error) {
	var replay []byte
	var status string
	err := conn.QueryRowContext(ctx, `SELECT replay_id, status FROM integration_sync_state WHERE source_system=$1 AND subscription_key=$2`, cdcSource, key).Scan(&replay, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	return replay, len(replay) == 0 || status == "recovery_required", nil
}

func requireCDCRecovery(ctx context.Context, conn *sql.Conn, key string) error {
	_, err := conn.ExecContext(ctx, `INSERT INTO integration_sync_state (source_system,subscription_key,status,updated_at) VALUES ($1,$2,'recovery_required',NOW()) ON CONFLICT (source_system,subscription_key) DO UPDATE SET status='recovery_required',updated_at=NOW()`, cdcSource, key)
	return err
}

func saveCDCCheckpoint(ctx context.Context, exec repositories.EventExecutor, key string, replay []byte) error {
	if len(replay) == 0 {
		return errors.New("empty CDC replay ID")
	}
	_, err := exec.ExecContext(ctx, `INSERT INTO integration_sync_state (source_system,subscription_key,replay_id,status,last_success_at,last_synced_at,updated_at) VALUES ($1,$2,$3,'healthy',NOW(),NOW(),NOW()) ON CONFLICT (source_system,subscription_key) DO UPDATE SET replay_id=EXCLUDED.replay_id,status='healthy',last_error=NULL,last_success_at=NOW(),last_synced_at=NOW(),updated_at=NOW()`, cdcSource, key, replay)
	return err
}

func decodeCDCEvent(ctx context.Context, service proto.PubSubClient, event *proto.ConsumerEvent) (map[string]any, error) {
	if event == nil || event.GetEvent() == nil {
		return nil, errors.New("missing CDC event")
	}
	schemaID := event.GetEvent().GetSchemaId()
	var codec *goavro.Codec
	if cached, ok := schemaCache.Load(schemaID); ok {
		codec = cached.(*goavro.Codec)
	} else {
		schema, err := service.GetSchema(ctx, &proto.SchemaRequest{SchemaId: schemaID})
		if err != nil {
			return nil, err
		}
		codec, err = goavro.NewCodec(schema.GetSchemaJson())
		if err != nil {
			return nil, err
		}
		schemaCache.Store(schemaID, codec)
	}
	native, _, err := codec.NativeFromBinary(event.GetEvent().GetPayload())
	if err != nil {
		return nil, err
	}
	payload, ok := native.(map[string]any)
	if !ok {
		return nil, errors.New("invalid CDC payload")
	}
	return payload, nil
}

func parseCDCHeader(payload map[string]any) (string, string, []string, error) {
	header, ok := payload["ChangeEventHeader"].(map[string]any)
	if !ok {
		return "", "", nil, errors.New("missing CDC header")
	}
	entity, _ := header["entityName"].(string)
	change, _ := unwrapAvro(header["changeType"]).(string)
	change = strings.ToUpper(change)
	if entity == "" || change == "" {
		return "", "", nil, errors.New("incomplete CDC header")
	}
	if strings.HasPrefix(change, "GAP_") {
		return entity, change, nil, errRecoveryRequired
	}
	raw, ok := header["recordIds"].([]any)
	if !ok || len(raw) == 0 {
		return "", "", nil, errors.New("missing CDC record IDs")
	}
	ids := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, v := range raw {
		id, ok := v.(string)
		if !ok || !validSFID(id) {
			return "", "", nil, errors.New("invalid CDC record ID")
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return entity, change, ids, nil
}

func processCDCMessage(ctx context.Context, conn *sql.Conn, client *sf.Client, key string, replay []byte, payload map[string]any) error {
	entity, change, ids, err := parseCDCHeader(payload)
	if err != nil {
		return err
	}
	mutations := make(map[string]*models.Event)
	deletions := make(map[string]time.Time)
	if entity == "Opportunity" {
		switch change {
		case "CREATE", "UPDATE", "UNDELETE", "DELETE":
		default:
			return fmt.Errorf("unsupported Opportunity change %s", change)
		}
		for _, id := range ids {
			if change == "DELETE" {
				header := payload["ChangeEventHeader"].(map[string]any)
				committed, ok := unwrapAvro(header["commitTimestamp"]).(int64)
				if !ok || committed <= 0 {
					return fmt.Errorf("DELETE missing valid commitTimestamp")
				}
				mutations[id] = nil
				deletions[id] = time.UnixMilli(committed).UTC()
				continue
			}
			event, err := sf.GetEventByIDContext(ctx, client, id)
			if errors.Is(err, sf.ErrEventNotFound) {
				deleted, version, checkErr := confirmedDeleted(ctx, client, id)
				if checkErr != nil {
					return checkErr
				}
				if !deleted {
					return fmt.Errorf("Opportunity %s unavailable; not advancing checkpoint", id)
				}
				mutations[id] = nil
				deletions[id] = version
				continue
			}
			if err != nil {
				return err
			}
			mutations[id] = event
		}
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		event, present := mutations[id]
		if !present {
			continue
		}
		if event == nil {
			err = repositories.MarkEventDeleted(ctx, tx, id, deletions[id])
		} else {
			err = repositories.UpsertEvent(ctx, tx, event)
		}
		if err != nil {
			return err
		}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO integration_inbox (source_system,message_key,replay_id,entity_name,change_type,source_record_id,payload,status) VALUES ($1,$2,$3,$4,$5,$6,$7,'processed')`, cdcSource, key, replay, entity, change, ids[0], string(data))
	if err != nil {
		return err
	}
	if err = saveCDCCheckpoint(ctx, tx, key, replay); err != nil {
		return err
	}
	return tx.Commit()
}
