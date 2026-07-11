package handlers

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/setucore/setu-cloud/internal/config"
	"github.com/setucore/setu-cloud/internal/keystore"
	"github.com/setucore/setu-cloud/internal/schema"
)

var adminMacRegex = regexp.MustCompile(`^[0-9a-f]{12}$`)

func adminErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// ── Released products store ──────────────────────────────────────────────────

// UpsertReleasedProduct ingests a released schema artifact pushed by dev_portal
// (POST /admin/released-products). Idempotent: re-pushing the same content
// (same tid/pid/version + content_hash) is a no-op upsert.
func UpsertReleasedProduct(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := readBody(r, 1<<20)
		if err != nil {
			adminErr(w, http.StatusBadRequest, "invalid body")
			return
		}
		art, err := schema.Parse(raw)
		if err != nil {
			adminErr(w, http.StatusBadRequest, "invalid schema json: "+err.Error())
			return
		}
		if art.TID == "" || art.PID == "" || art.Version <= 0 || art.ContentHash == "" {
			adminErr(w, http.StatusUnprocessableEntity, "tid, pid, version and content_hash are required")
			return
		}
		publishedAt := time.Now().UTC()
		if art.PublishedAt != "" {
			if t, perr := time.Parse(time.RFC3339, art.PublishedAt); perr == nil {
				publishedAt = t
			}
		}

		_, err = db.Exec(r.Context(), `
			INSERT INTO released_products (tid, pid, version, schema_json, content_hash, published_at)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (tid, pid, version)
			DO UPDATE SET schema_json  = EXCLUDED.schema_json,
			              content_hash = EXCLUDED.content_hash,
			              published_at = EXCLUDED.published_at,
			              received_at  = NOW()
		`, art.TID, art.PID, art.Version, raw, art.ContentHash, publishedAt)
		if err != nil {
			adminErr(w, http.StatusInternalServerError, "could not store released product: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"tid": art.TID, "pid": art.PID, "version": art.Version,
			"content_hash": art.ContentHash, "status": "stored",
		})
	}
}

type releasedProductDTO struct {
	TID         string          `json:"tid"`
	PID         string          `json:"pid"`
	Version     int             `json:"version"`
	ContentHash string          `json:"content_hash"`
	PublishedAt time.Time       `json:"published_at"`
	ReceivedAt  time.Time       `json:"received_at"`
	RetiredAt   *time.Time      `json:"retired_at"`
	Schema      json.RawMessage `json:"schema_json"`
}

// ListReleasedProducts serves GET /admin/released-products?tid=&pid= for the
// admin portal. Both filters are optional; results are newest version first.
func ListReleasedProducts(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tid := r.URL.Query().Get("tid")
		pid := r.URL.Query().Get("pid")
		rows, err := db.Query(r.Context(), `
			SELECT tid, pid, version, content_hash, published_at, received_at, retired_at, schema_json
			  FROM released_products
			 WHERE ($1 = '' OR tid = $1) AND ($2 = '' OR pid = $2)
			 ORDER BY tid, pid, version DESC
		`, tid, pid)
		if err != nil {
			adminErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()
		out := []releasedProductDTO{}
		for rows.Next() {
			var d releasedProductDTO
			if err := rows.Scan(&d.TID, &d.PID, &d.Version, &d.ContentHash,
				&d.PublishedAt, &d.ReceivedAt, &d.RetiredAt, &d.Schema); err != nil {
				adminErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			out = append(out, d)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// RetireReleasedProduct toggles the soft-retire flag on a released product
// (POST /admin/released-products/retire). Body: {tid, pid, version?, retired}.
// Without `version` it applies to every version of (tid, pid). Used both by the
// dev portal (auto-retire when a released product is deleted) and by staff in
// the admin portal (manual retire / restore).
func RetireReleasedProduct(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			TID     string `json:"tid"`
			PID     string `json:"pid"`
			Version *int   `json:"version"`
			Retired bool   `json:"retired"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			adminErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if req.TID == "" || req.PID == "" {
			adminErr(w, http.StatusUnprocessableEntity, "tid and pid are required")
			return
		}
		// retired_at = NOW() when retiring, NULL when restoring.
		var retiredAt any
		if req.Retired {
			retiredAt = time.Now().UTC()
		}
		tag, err := db.Exec(r.Context(), `
			UPDATE released_products SET retired_at = $1
			 WHERE tid = $2 AND pid = $3 AND ($4::int IS NULL OR version = $4)`,
			retiredAt, req.TID, req.PID, req.Version)
		if err != nil {
			adminErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"tid": req.TID, "pid": req.PID, "retired": req.Retired, "updated": tag.RowsAffected(),
		})
	}
}

// ── Inventory seeding ────────────────────────────────────────────────────────

type createBatchReq struct {
	TID            string   `json:"tid"`
	PID            string   `json:"pid"`
	SchemaVersion  int      `json:"schema_version"`
	MACs           []string `json:"macs"`
	Qty            int      `json:"qty"`
	IntegratorNote string   `json:"integrator_note"`
	CreatedBy      string   `json:"created_by"`
}

type seededDevice struct {
	DID    string `json:"did"`
	MAC    string `json:"mac,omitempty"`
	MQUser string `json:"mq_user"`
	MQPass string `json:"mq_pass"`
	Status string `json:"status"`
}

// CreateBatch seeds a manufacturing batch (POST /admin/inventory/batches). It
// allocates a DID + MQTT credentials per device against a released schema and
// records them as status=inventory. With explicit MACs each device is bound to
// its MAC now; with a bare qty the DIDs are reserved (mac NULL) to be bound at
// provision time.
func CreateBatch(db *pgxpool.Pool, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createBatchReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			adminErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if req.TID == "" || req.PID == "" || req.SchemaVersion <= 0 {
			adminErr(w, http.StatusUnprocessableEntity, "tid, pid and schema_version are required")
			return
		}

		// The (tid, pid, schema_version) must reference a released schema.
		var schemaJSON []byte
		err := db.QueryRow(r.Context(),
			`SELECT schema_json FROM released_products WHERE tid=$1 AND pid=$2 AND version=$3`,
			req.TID, req.PID, req.SchemaVersion).Scan(&schemaJSON)
		if err == pgx.ErrNoRows {
			adminErr(w, http.StatusUnprocessableEntity,
				fmt.Sprintf("no released schema for tid=%s pid=%s version=%d", req.TID, req.PID, req.SchemaVersion))
			return
		} else if err != nil {
			adminErr(w, http.StatusInternalServerError, err.Error())
			return
		}

		// Project the firmware hw_config once; stored per device as a convenience
		// (ZTP re-projects authoritatively from the released schema at provision).
		art, err := schema.Parse(schemaJSON)
		if err != nil {
			adminErr(w, http.StatusInternalServerError, "stored schema is corrupt: "+err.Error())
			return
		}
		hwConfig, _ := json.Marshal(art.FirmwareConfig())

		// Normalise the work list: explicit MACs win over a bare qty.
		var macs []string
		seen := map[string]bool{}
		for _, m := range req.MACs {
			nm := normMAC(m)
			if !adminMacRegex.MatchString(nm) {
				adminErr(w, http.StatusBadRequest, "invalid MAC: "+m)
				return
			}
			if !seen[nm] {
				seen[nm] = true
				macs = append(macs, nm)
			}
		}
		qty := len(macs)
		if qty == 0 {
			qty = req.Qty
		}
		if qty <= 0 {
			adminErr(w, http.StatusUnprocessableEntity, "provide a non-empty macs[] or a positive qty")
			return
		}

		ctx := r.Context()
		// Ensure the tenant exists and has a single shared MQTT credential. Every
		// device under this TID uses the same mq_user/mq_pass. The atomic upsert
		// (COALESCE keeps any existing creds) makes concurrent batches converge on
		// one credential pair. The released schema proves the tid is real.
		var mqUser, mqPass string
		if err := db.QueryRow(ctx, `
			INSERT INTO tenants (tid, name, api_key_hash, mq_user, mq_pass)
			VALUES ($1, $1, $2, $1, $3)
			ON CONFLICT (tid) DO UPDATE
			  SET mq_user = COALESCE(tenants.mq_user, EXCLUDED.mq_user),
			      mq_pass = COALESCE(tenants.mq_pass, EXCLUDED.mq_pass)
			RETURNING mq_user, mq_pass`,
			req.TID, "seeded:"+genSecret(8), genSecret(16)).Scan(&mqUser, &mqPass); err != nil {
			adminErr(w, http.StatusInternalServerError, "could not resolve tenant mqtt credential: "+err.Error())
			return
		}

		tx, err := db.Begin(ctx)
		if err != nil {
			adminErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer tx.Rollback(ctx)

		batchID := uuid.New().String()
		createdBy := req.CreatedBy
		if createdBy == "" {
			createdBy = "service"
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO batches (id, tid, pid, schema_version, qty, integrator_note, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			batchID, req.TID, req.PID, req.SchemaVersion, qty, req.IntegratorNote, createdBy); err != nil {
			adminErr(w, http.StatusInternalServerError, "could not create batch: "+err.Error())
			return
		}

		devices := make([]seededDevice, 0, qty)
		for idx := 0; idx < qty; idx++ {
			did := uuid.New().String()
			// Shared per-tenant MQTT credential (same for every device under this TID).

			var macVal, bleVal any // nil for reserved (qty-only) devices
			d := seededDevice{DID: did, MQUser: mqUser, MQPass: mqPass, Status: "inventory"}
			if idx < len(macs) {
				macVal = macs[idx]
				bleVal = macPlusN(macs[idx], 2)
				d.MAC = macs[idx]
			}

			if _, err := tx.Exec(ctx, `
				INSERT INTO device_inventory
				   (mac, ble_mac, tid, did, pid, hw_config, schema_version, status, batch_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7, 'inventory', $8)`,
				macVal, bleVal, req.TID, did, req.PID, hwConfig, req.SchemaVersion, batchID); err != nil {
				if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
					adminErr(w, http.StatusConflict, "a MAC in this batch is already in inventory: "+err.Error())
					return
				}
				adminErr(w, http.StatusInternalServerError, "could not seed device: "+err.Error())
				return
			}
			devices = append(devices, d)
		}

		if err := tx.Commit(ctx); err != nil {
			adminErr(w, http.StatusInternalServerError, err.Error())
			return
		}

		// Best-effort EMQX provisioning — one shared user per tenant (idempotent).
		emqxStatus := "skipped (EMQX not configured)"
		if cfg.EMQXKey != "" {
			if err := createEMQXUser(cfg, mqUser, mqPass); err != nil {
				emqxStatus = "error: " + err.Error()
			} else {
				emqxStatus = "ok"
			}
		}

		writeJSON(w, http.StatusCreated, map[string]any{
			"batch_id":       batchID,
			"tid":            req.TID,
			"pid":            req.PID,
			"schema_version": req.SchemaVersion,
			"count":          len(devices),
			"reserved":       qty - len(macs),
			"mq_user":        mqUser,
			"devices":        devices,
			"emqx":           map[string]any{"status": emqxStatus},
		})
	}
}

type inventoryDTO struct {
	MAC           *string    `json:"mac"`
	DID           string     `json:"did"`
	TID           string     `json:"tid"`
	PID           string     `json:"pid"`
	SchemaVersion *int       `json:"schema_version"`
	Status        string     `json:"status"`
	BatchID       *string    `json:"batch_id"`
	MQUser        string     `json:"mq_user"`
	ProvisionedAt *time.Time `json:"provisioned_at"`
	RegisteredAt  time.Time  `json:"registered_at"`
}

// ListInventory serves GET /admin/inventory?tid=&pid=&status= with a per-status
// count summary.
func ListInventory(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		tid, pid, status, batch := q.Get("tid"), q.Get("pid"), q.Get("status"), q.Get("batch")

		rows, err := db.Query(r.Context(), `
			SELECT di.mac, di.did, di.tid, di.pid, di.schema_version, di.status, di.batch_id::text,
			       COALESCE(t.mq_user, ''), di.provisioned_at, di.registered_at
			  FROM device_inventory di JOIN tenants t ON t.tid = di.tid
			 WHERE ($1='' OR di.tid=$1) AND ($2='' OR di.pid=$2) AND ($3='' OR di.status=$3)
			   AND ($4=''::text OR di.batch_id::text=$4)
			 ORDER BY di.registered_at DESC`, tid, pid, status, batch)
		if err != nil {
			adminErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()
		out := []inventoryDTO{}
		for rows.Next() {
			var d inventoryDTO
			if err := rows.Scan(&d.MAC, &d.DID, &d.TID, &d.PID, &d.SchemaVersion,
				&d.Status, &d.BatchID, &d.MQUser, &d.ProvisionedAt, &d.RegisteredAt); err != nil {
				adminErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			out = append(out, d)
		}

		// Counts per status, honouring the tid/pid filters (but not status).
		counts := map[string]int{}
		crows, err := db.Query(r.Context(), `
			SELECT status, count(*) FROM device_inventory
			 WHERE ($1='' OR tid=$1) AND ($2='' OR pid=$2)
			 GROUP BY status`, tid, pid)
		if err == nil {
			defer crows.Close()
			for crows.Next() {
				var s string
				var n int
				if crows.Scan(&s, &n) == nil {
					counts[s] = n
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"devices": out, "counts": counts, "total": len(out),
		})
	}
}

type batchDTO struct {
	ID             string    `json:"id"`
	TID            string    `json:"tid"`
	PID            string    `json:"pid"`
	SchemaVersion  int       `json:"schema_version"`
	Qty            int       `json:"qty"`
	IntegratorNote string    `json:"integrator_note"`
	CreatedBy      string    `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
	Seeded         int       `json:"seeded"` // device_inventory rows actually created for this batch
}

// ListBatches serves GET /admin/batches?tid=&pid= — the batch history, newest
// first, with the count of inventory rows seeded under each batch.
func ListBatches(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tid, pid := r.URL.Query().Get("tid"), r.URL.Query().Get("pid")
		rows, err := db.Query(r.Context(), `
			SELECT b.id::text, b.tid, b.pid, b.schema_version, b.qty,
			       b.integrator_note, b.created_by, b.created_at, count(di.did) AS seeded
			  FROM batches b
			  LEFT JOIN device_inventory di ON di.batch_id = b.id
			 WHERE ($1='' OR b.tid=$1) AND ($2='' OR b.pid=$2)
			 GROUP BY b.id
			 ORDER BY b.created_at DESC`, tid, pid)
		if err != nil {
			adminErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()
		out := []batchDTO{}
		for rows.Next() {
			var b batchDTO
			if err := rows.Scan(&b.ID, &b.TID, &b.PID, &b.SchemaVersion, &b.Qty,
				&b.IntegratorNote, &b.CreatedBy, &b.CreatedAt, &b.Seeded); err != nil {
				adminErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			out = append(out, b)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// ztpPreview mirrors the ZTP /factory/provision bundle a device receives.
type ztpPreview struct {
	Preview       bool            `json:"preview"`
	DID           string          `json:"did"`
	TID           string          `json:"tid"`
	PID           string          `json:"pid"`
	MAC           *string         `json:"mac"`
	Status        string          `json:"status"`
	MQUser        string          `json:"mq_user"`
	MQPass        string          `json:"mq_pass"`
	MQURI         string          `json:"mq_uri"`
	CloudPubkey   string          `json:"cloud_pubkey"`
	SchemaVersion *int            `json:"schema_version,omitempty"`
	HWConfig      json.RawMessage `json:"hw_config"`
}

// PreviewProvision serves GET /admin/inventory/{did}/ztp — a READ-ONLY rendering
// of the exact provision bundle this device receives at ZTP (firmware hw_config
// projected from its bound released schema, MQTT creds, cloud pubkey, etc.).
// Unlike POST /factory/provision it does NOT claim/mutate the device.
func PreviewProvision(db *pgxpool.Pool, cfg *config.Config, ks *keystore.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		did := chi.URLParam(r, "did")
		ctx := r.Context()

		var (
			resp     ztpPreview
			storedHW []byte
		)
		err := db.QueryRow(ctx, `
			SELECT di.mac, di.tid, di.did, di.pid,
			       COALESCE(t.mq_user, ''), COALESCE(t.mq_pass, ''),
			       di.hw_config, di.schema_version, di.status
			  FROM device_inventory di JOIN tenants t ON t.tid = di.tid
			 WHERE di.did = $1`, did).
			Scan(&resp.MAC, &resp.TID, &resp.DID, &resp.PID, &resp.MQUser, &resp.MQPass,
				&storedHW, &resp.SchemaVersion, &resp.Status)
		if err == pgx.ErrNoRows {
			adminErr(w, http.StatusNotFound, "device not found")
			return
		} else if err != nil {
			adminErr(w, http.StatusInternalServerError, err.Error())
			return
		}

		resp.Preview = true
		resp.MQURI = cfg.DeviceMQTTBrokerURI

		// Cloud public key for the device's tenant (keystore, with legacy fallback).
		resp.CloudPubkey = cfg.CloudPubkeyHex
		if ks != nil {
			if pk, e := ks.ActivePubKey(ctx, resp.TID); e == nil && pk != "" {
				resp.CloudPubkey = pk
			}
		}

		// hw_config: project from the bound released schema (authoritative), else
		// fall back to the per-device stored column — exactly like ZTP does.
		hw := storedHW
		if resp.SchemaVersion != nil {
			var raw []byte
			if e := db.QueryRow(ctx,
				`SELECT schema_json FROM released_products WHERE tid=$1 AND pid=$2 AND version=$3`,
				resp.TID, resp.PID, *resp.SchemaVersion).Scan(&raw); e == nil {
				if art, e2 := schema.Parse(raw); e2 == nil {
					if b, e3 := json.Marshal(art.FirmwareConfig()); e3 == nil {
						hw = b
					}
				}
			}
		}
		if len(hw) == 0 {
			hw = []byte("{}")
		}
		resp.HWConfig = hw

		writeJSON(w, http.StatusOK, resp)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func readBody(r *http.Request, max int64) ([]byte, error) {
	defer r.Body.Close()
	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(http.MaxBytesReader(nil, r.Body, max))
	return buf.Bytes(), err
}

func normMAC(s string) string {
	return strings.ToLower(strings.NewReplacer(":", "", "-", "").Replace(strings.TrimSpace(s)))
}

func genSecret(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// macPlusN increments a 12-char hex MAC by n (ESP32 BLE MAC = Wi-Fi MAC + 2).
func macPlusN(mac string, n uint64) string {
	b, err := hex.DecodeString(mac)
	if err != nil || len(b) != 6 {
		return ""
	}
	padded := make([]byte, 8)
	copy(padded[2:], b)
	val := binary.BigEndian.Uint64(padded) + n
	binary.BigEndian.PutUint64(padded, val)
	return hex.EncodeToString(padded[2:])
}

func createEMQXUser(cfg *config.Config, userID, password string) error {
	body, _ := json.Marshal(map[string]string{"user_id": userID, "password": password})
	req, _ := http.NewRequest(http.MethodPost,
		cfg.EMQXBaseURL+"/api/v5/authentication/password_based:built_in_database/users",
		bytes.NewReader(body))
	req.SetBasicAuth(cfg.EMQXKey, cfg.EMQXSecret)
	req.Header.Set("Content-Type", "application/json")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// The per-tenant MQTT user is shared, so re-seeding the same tenant POSTs an
	// existing user — EMQX returns 409 ALREADY_EXISTS, which is success for us.
	if resp.StatusCode == http.StatusConflict {
		return nil
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("EMQX returned %d", resp.StatusCode)
	}
	return nil
}
