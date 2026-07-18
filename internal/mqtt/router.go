package mqtt

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/setucore/setu-cloud/internal/registry"
	"github.com/setucore/setu-cloud/internal/shadow"
)

const workerPoolSize = 64

// envelope is the common wrapper for all /up messages.
type envelope struct {
	V   string          `json:"v"`
	C   string          `json:"c"`
	ID  string          `json:"id"`
	T   int64           `json:"t"`
	PID string          `json:"pid"`
	D   json.RawMessage `json:"d"`
}

// shdMessage is the /shd retained shadow payload.
type shdMessage struct {
	V      string          `json:"v"`
	T      int64           `json:"t"`
	Online bool            `json:"online"`
	PID    string          `json:"pid"`
	DID    string          `json:"did"`
	RSSI   int             `json:"rssi"`
	DPS    json.RawMessage `json:"dps"`
}

// Router dispatches incoming MQTT messages to the appropriate handlers.
type Router struct {
	db         *pgxpool.Pool
	cache      *redis.Client
	registry   *registry.Service
	shadow     *shadow.Service
	workers    chan struct{} // semaphore — limits concurrent DB writers
	tenantSeen sync.Map      // tid -> struct{}: cache of usernames known to be tenants
}

func NewRouter(db *pgxpool.Pool, cache *redis.Client) *Router {
	return &Router{
		db:       db,
		cache:    cache,
		registry: registry.New(db, cache),
		shadow:   shadow.New(db, cache),
		workers:  make(chan struct{}, workerPoolSize),
	}
}

// isKnownTenant reports whether tid is a real tenant. Device MQTT usernames are
// the tid, so this distinguishes a device connection from the cloud backend (or
// any other non-tenant client) in $SYS events. Positive results are cached.
func (r *Router) isKnownTenant(ctx context.Context, tid string) bool {
	if tid == "" {
		return false
	}
	if _, ok := r.tenantSeen.Load(tid); ok {
		return true
	}
	var exists bool
	if err := r.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenants WHERE tid=$1)`, tid).Scan(&exists); err != nil {
		log.Printf("isKnownTenant tid=%s: %v", tid, err)
		return false
	}
	if exists {
		r.tenantSeen.Store(tid, struct{}{})
	}
	return exists
}

// parseTopic extracts tid, pid, did from setu/{tid}/{pid}/{did}/{suffix}.
func parseTopic(topic string) (tid, pid, did, suffix string, ok bool) {
	parts := strings.Split(topic, "/")
	if len(parts) != 5 || parts[0] != "setu" {
		return
	}
	return parts[1], parts[2], parts[3], parts[4], true
}

// HandleUp processes messages on setu/+/+/+/up.
// Payload is copied before spawning a goroutine so the Paho callback
// can return immediately without blocking the broker's inbound pipeline.
func (r *Router) HandleUp(_ pahomqtt.Client, msg pahomqtt.Message) {
	payload := cloneBytes(msg.Payload())
	topic := msg.Topic()
	go func() {
		r.workers <- struct{}{}
		defer func() { <-r.workers }()
		r.processUp(topic, payload)
	}()
}

// HandleShd processes retained shadow messages on setu/+/+/+/shd.
func (r *Router) HandleShd(_ pahomqtt.Client, msg pahomqtt.Message) {
	payload := cloneBytes(msg.Payload())
	topic := msg.Topic()
	go func() {
		r.workers <- struct{}{}
		defer func() { <-r.workers }()
		r.processShd(topic, payload)
	}()
}

func (r *Router) processUp(topic string, payload []byte) {
	tid, _, did, _, ok := parseTopic(topic)
	if !ok {
		return
	}

	var env envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		log.Printf("mqtt/up parse error tid=%s did=%s: %v", tid, did, err)
		return
	}

	ctx := context.Background()
	switch env.C {
	case "reg":
		r.handleReg(ctx, tid, did, env)
	case "boo":
		r.handleBoo(ctx, tid, did, env)
	case "rpt":
		r.handleRpt(ctx, tid, did, env)
	case "ack":
		r.handleAck(ctx, tid, did, env)
	case "ota_done", "ota_err":
		r.handleOTA(ctx, tid, did, env)
	case "offline":
		r.handleOffline(ctx, tid, did, env)
	default:
		log.Printf("mqtt/up unknown command %q tid=%s did=%s", env.C, tid, did)
	}
}

func (r *Router) processShd(topic string, payload []byte) {
	tid, _, did, _, ok := parseTopic(topic)
	if !ok {
		return
	}

	var shd shdMessage
	if err := json.Unmarshal(payload, &shd); err != nil {
		log.Printf("mqtt/shd parse error tid=%s did=%s: %v", tid, did, err)
		return
	}

	ctx := context.Background()

	if err := r.shadow.HandleShadow(ctx, tid, did, shd.DPS, shd.RSSI, shd.T); err != nil {
		log.Printf("mqtt/shd shadow update tid=%s did=%s: %v", tid, did, err)
	}

	// Key off the explicit online bool from the shadow — NOT timestamp freshness.
	// Firmware sends online:false in its LWT /shd (with no t field), so the bool
	// is the correct signal. Timestamp-based detection breaks for idle devices
	// that have no periodic heartbeat (their t stays at connect-time value).
	// $SYS connect/disconnect events (below) are the primary online source;
	// this just fast-paths the LWT case and updates shadow state.
	if err := r.registry.SetOnlineCached(ctx, tid, did, shd.Online); err != nil {
		log.Printf("mqtt/shd set online cache tid=%s did=%s: %v", tid, did, err)
	}
}

// diagPayload is the optional crash/heap telemetry object piggybacked on
// reg/boo by firmware builds with CONFIG_SETU_DIAGNOSTICS_ENABLED +
// the runtime reporting toggle both on. Absent entirely otherwise, so
// diag stays nil and updateDiag is skipped — no schema assumption made
// about older or diagnostics-disabled devices.
type diagPayload struct {
	RR string `json:"rr"`
	CC int    `json:"cc"`
	HP int    `json:"hp"`
}

func (r *Router) updateDiag(ctx context.Context, tid, did string, diag *diagPayload) {
	if diag == nil {
		return
	}
	if err := r.registry.UpdateDiag(ctx, tid, did, diag.RR, diag.CC, diag.HP); err != nil {
		log.Printf("registry update diag tid=%s did=%s: %v", tid, did, err)
	}
}

func (r *Router) handleReg(ctx context.Context, tid, did string, env envelope) {
	var d struct {
		RSSI  int          `json:"rssi"`
		FWVer string       `json:"fw_ver"`
		Diag  *diagPayload `json:"diag"`
	}
	json.Unmarshal(env.D, &d)

	if err := r.registry.Upsert(ctx, tid, did, env.PID, d.FWVer, d.RSSI); err != nil {
		log.Printf("registry upsert reg tid=%s did=%s: %v", tid, did, err)
	}
	// Upsert already sets is_online=true in DB; mirror to Redis.
	r.registry.SetOnlineCached(ctx, tid, did, true)
	r.updateDiag(ctx, tid, did, d.Diag)
	r.storeEvent(ctx, tid, did, "reg", env.D)
	r.publishWS(ctx, tid, wsEvent{Type: "reg", DID: did, TID: tid, T: env.T, Data: env.D})
}

func (r *Router) handleBoo(ctx context.Context, tid, did string, env envelope) {
	var d struct {
		RSSI  int          `json:"rssi"`
		FWVer string       `json:"fw_ver"`
		Diag  *diagPayload `json:"diag"`
	}
	json.Unmarshal(env.D, &d)

	// boo = explicit reconnect event — update both DB and Redis.
	if err := r.registry.SetOnline(ctx, tid, did, true); err != nil {
		log.Printf("registry set online boo tid=%s did=%s: %v", tid, did, err)
	}
	r.registry.SetOnlineCached(ctx, tid, did, true)
	r.updateDiag(ctx, tid, did, d.Diag)
	r.storeEvent(ctx, tid, did, "boo", env.D)
	r.publishWS(ctx, tid, wsEvent{Type: "boo", DID: did, TID: tid, T: env.T, Data: env.D})
}

func (r *Router) handleRpt(ctx context.Context, tid, did string, env envelope) {
	if err := r.shadow.UpdateReported(ctx, tid, did, env.D); err != nil {
		log.Printf("shadow update rpt tid=%s did=%s: %v", tid, did, err)
	}
	r.storeEvent(ctx, tid, did, "rpt", env.D)
	r.publishWS(ctx, tid, wsEvent{Type: "rpt", DID: did, TID: tid, T: env.T, Data: env.D})
}

func (r *Router) handleAck(ctx context.Context, tid, did string, env envelope) {
	// ec/reason are new, optional fields (setu_errors.h taxonomy) — absent
	// entirely on older firmware or a bare {"ok":true} success ack, so they
	// stay nil/zero and the UPDATE below just leaves the columns NULL.
	var d struct {
		OK     bool   `json:"ok"`
		EC     *int   `json:"ec"`
		Reason string `json:"reason"`
	}
	json.Unmarshal(env.D, &d)

	status := "acked_ok"
	if !d.OK {
		status = "acked_fail"
	}
	var reason *string
	if d.Reason != "" {
		reason = &d.Reason
	}
	if _, err := r.db.Exec(ctx,
		`UPDATE commands SET status=$1, acked_at=NOW(), ack_ec=$2, ack_reason=$3 WHERE id=$4 AND tid=$5`,
		status, d.EC, reason, env.ID, tid,
	); err != nil {
		log.Printf("command ack update tid=%s did=%s id=%s: %v", tid, did, env.ID, err)
	}
	r.storeEvent(ctx, tid, did, "ack", env.D)
	r.publishWS(ctx, tid, wsEvent{Type: "ack", DID: did, TID: tid, T: env.T, Data: env.D})
}

func (r *Router) handleOTA(ctx context.Context, tid, did string, env envelope) {
	status := "acked_ok"
	if env.C == "ota_err" {
		status = "acked_fail"
	}
	if _, err := r.db.Exec(ctx,
		`UPDATE commands SET status=$1, acked_at=NOW() WHERE id=$2 AND tid=$3`,
		status, env.ID, tid,
	); err != nil {
		log.Printf("ota ack update tid=%s did=%s: %v", tid, did, err)
	}
	r.storeEvent(ctx, tid, did, env.C, env.D)
	r.publishWS(ctx, tid, wsEvent{Type: env.C, DID: did, TID: tid, T: env.T, Data: env.D})
}

func (r *Router) handleOffline(ctx context.Context, tid, did string, env envelope) {
	// Fired by EMQX via LWT when the device drops unexpectedly,
	// or sent explicitly by the device before a clean shutdown.
	if err := r.registry.SetOnline(ctx, tid, did, false); err != nil {
		log.Printf("registry set offline tid=%s did=%s: %v", tid, did, err)
	}
	// Delete the Redis TTL key immediately — no waiting for expiry.
	if err := r.registry.SetOnlineCached(ctx, tid, did, false); err != nil {
		log.Printf("registry set offline cache tid=%s did=%s: %v", tid, did, err)
	}
	// Push to any connected WebSocket clients so the app tile updates instantly.
	r.publishWS(ctx, tid, wsEvent{Type: "offline", DID: did, TID: tid, T: env.T})
	log.Printf("device offline tid=%s did=%s", tid, did)
}

func (r *Router) storeEvent(ctx context.Context, tid, did, evType string, payload json.RawMessage) {
	if _, err := r.db.Exec(ctx,
		`INSERT INTO device_events (tid, did, event_type, payload) VALUES ($1, $2, $3, $4)`,
		tid, did, evType, []byte(payload),
	); err != nil {
		log.Printf("event store tid=%s did=%s type=%s: %v", tid, did, evType, err)
	}
}

type wsEvent struct {
	Type string          `json:"type"`
	TID  string          `json:"tid"`
	DID  string          `json:"did"`
	T    int64           `json:"t"`
	Data json.RawMessage `json:"data,omitempty"`
}

func (r *Router) publishWS(ctx context.Context, tid string, evt wsEvent) {
	b, _ := json.Marshal(evt)
	r.cache.Publish(ctx, "ws:events:"+tid, string(b))
}

// ── EMQX $SYS connect/disconnect handlers ────────────────────────────────────

// sysEvent is the common subset of EMQX's $SYS connect/disconnect payloads.
// MQTT credentials are shared per tenant, so EMQX publishes username = tid for
// every device; the device is identified by clientid = did.
type sysEvent struct {
	ClientID string `json:"clientid"`
	Username string `json:"username"`
}

// deviceIdentity resolves a $SYS event to (tid, did) for a provisioned device,
// or ok=false for non-device clients (e.g. the cloud backend). tid is the
// username (validated against tenants); did is the clientid.
func (r *Router) deviceIdentity(ctx context.Context, ev sysEvent) (tid, did string, ok bool) {
	if ev.ClientID == "" || !r.isKnownTenant(ctx, ev.Username) {
		return "", "", false
	}
	return ev.Username, ev.ClientID, true
}

// HandleSysConnected processes $SYS/brokers/+/clients/+/connected.
func (r *Router) HandleSysConnected(_ pahomqtt.Client, msg pahomqtt.Message) {
	payload := cloneBytes(msg.Payload())
	go func() {
		r.workers <- struct{}{}
		defer func() { <-r.workers }()

		var ev sysEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return
		}
		ctx := context.Background()
		tid, did, ok := r.deviceIdentity(ctx, ev)
		if !ok {
			return // not a device connection (e.g. cloud_backend)
		}

		// No TTL — device stays online until an explicit disconnect/LWT.
		if err := r.registry.SetOnlineCached(ctx, tid, did, true); err != nil {
			log.Printf("sys/connected set online cache tid=%s did=%s: %v", tid, did, err)
		}
		log.Printf("sys/connected tid=%s did=%s", tid, did)
	}()
}

// HandleSysDisconnected processes $SYS/brokers/+/clients/+/disconnected.
func (r *Router) HandleSysDisconnected(_ pahomqtt.Client, msg pahomqtt.Message) {
	payload := cloneBytes(msg.Payload())
	go func() {
		r.workers <- struct{}{}
		defer func() { <-r.workers }()

		var ev sysEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return
		}
		ctx := context.Background()
		tid, did, ok := r.deviceIdentity(ctx, ev)
		if !ok {
			return
		}

		if err := r.registry.SetOnline(ctx, tid, did, false); err != nil {
			log.Printf("sys/disconnected set offline tid=%s did=%s: %v", tid, did, err)
		}
		if err := r.registry.SetOnlineCached(ctx, tid, did, false); err != nil {
			log.Printf("sys/disconnected set offline cache tid=%s did=%s: %v", tid, did, err)
		}
		r.publishWS(ctx, tid, wsEvent{
			Type: "offline", DID: did, TID: tid, T: time.Now().Unix(),
		})
		log.Printf("sys/disconnected tid=%s did=%s", tid, did)
	}()
}

func cloneBytes(b []byte) []byte {
	c := make([]byte, len(b))
	copy(c, b)
	return c
}
