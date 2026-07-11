package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/setucore/setu-cloud/internal/keystore"
)

//go:embed ui
var uiFS embed.FS

var macRegex = regexp.MustCompile(`^[0-9a-f]{12}$`)

const sessionCookie = "setu_admin_session"
const sessionTTL = 8 * time.Hour

type sessionData struct {
	UserID    string
	Username  string
	ExpiresAt time.Time
}

type server struct {
	db         *pgxpool.Pool
	tid        string
	emqxBase   string
	emqxKey    string
	emqxSecret string
	ks         *keystore.Service // nil when KEY_ENCRYPTION_KEY not configured

	cloudBase string // setu-cloud API base URL for /admin/* proxying
	svcToken  string // ADMIN_SERVICE_TOKEN attached server-side to cloud /admin/* calls

	mu       sync.RWMutex
	sessions map[string]sessionData
}

type device struct {
	MAC           string          `json:"mac"`
	TID           string          `json:"tid"`
	DID           string          `json:"did"`
	PID           string          `json:"pid"`
	MQUser        string          `json:"mq_user"`
	HWConfig      json.RawMessage `json:"hw_config"`
	RegisteredAt  time.Time       `json:"registered_at"`
	ProvisionedAt *time.Time      `json:"provisioned_at"`
}

type adminUser struct {
	ID          string     `json:"id"`
	Username    string     `json:"username"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at"`
}

func main() {
	addr := flag.String("addr", ":9090", "listen address")
	createAdmin := flag.String("create-admin", "", "bootstrap an admin user — format: username:password")
	flag.Parse()

	pool, err := pgxpool.New(context.Background(), mustEnv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()

	// Bootstrap mode: create/update an admin user then exit.
	if *createAdmin != "" {
		parts := strings.SplitN(*createAdmin, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			log.Fatal("usage: --create-admin username:password")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(parts[1]), bcrypt.DefaultCost)
		if err != nil {
			log.Fatalf("bcrypt: %v", err)
		}
		_, err = pool.Exec(context.Background(),
			`INSERT INTO admin_users (id, username, password_hash)
			 VALUES ($1, $2, $3)
			 ON CONFLICT (username) DO UPDATE SET password_hash = $3`,
			uuid.New().String(), parts[0], string(hash))
		if err != nil {
			log.Fatalf("create admin user: %v", err)
		}
		log.Printf("admin user %q created/updated", parts[0])
		return
	}

	// Warn if no admin users exist yet.
	var count int
	pool.QueryRow(context.Background(), `SELECT count(*) FROM admin_users`).Scan(&count)
	if count == 0 {
		log.Println("WARNING: no admin users found. Create one with:")
		log.Println("  ./bin/setu-admin --create-admin admin:yourpassword")
	}

	// Keystore is optional — if KEY_ENCRYPTION_KEY is not set, the Tenant Keys
	// tab in the admin UI will show a warning instead of key data.
	var ks *keystore.Service
	if kekHex := os.Getenv("KEY_ENCRYPTION_KEY"); kekHex != "" {
		kek, err := hex.DecodeString(kekHex)
		if err == nil && len(kek) == 32 {
			ks, err = keystore.New(pool, kek)
			if err != nil {
				log.Printf("keystore init: %v", err)
			}
		} else {
			log.Println("WARNING: KEY_ENCRYPTION_KEY must be 64 hex chars — tenant key management disabled")
		}
	}

	srv := &server{
		db:         pool,
		tid:        env("CONSUMER_TID", "setu"),
		emqxBase:   env("EMQX_API_URL", "http://localhost:18083"),
		emqxKey:    os.Getenv("EMQX_API_KEY"),
		emqxSecret: os.Getenv("EMQX_API_SECRET"),
		ks:         ks,
		cloudBase:  env("CLOUD_API_URL", "http://127.0.0.1:8080"),
		svcToken:   os.Getenv("ADMIN_SERVICE_TOKEN"),
		sessions:   make(map[string]sessionData),
	}
	if srv.svcToken == "" {
		log.Println("WARNING: ADMIN_SERVICE_TOKEN not set — released-products / inventory tabs will fail (cloud /admin/* returns 401)")
	}

	// Periodically purge expired sessions.
	go func() {
		for range time.Tick(time.Hour) {
			srv.mu.Lock()
			for tok, s := range srv.sessions {
				if time.Now().After(s.ExpiresAt) {
					delete(srv.sessions, tok)
				}
			}
			srv.mu.Unlock()
		}
	}()

	sub, _ := fs.Sub(uiFS, "ui")
	mux := http.NewServeMux()

	// Public
	mux.HandleFunc("GET /login", srv.serveLogin)
	mux.HandleFunc("POST /login", srv.handleLogin)
	mux.HandleFunc("POST /logout", srv.handleLogout)

	// Protected — all other routes go through srv.auth()
	protected := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, srv.auth(h))
	}
	mux.Handle("GET /", srv.auth(http.FileServer(http.FS(sub))))

	protected("GET /api/me", srv.handleMe)

	protected("GET /api/devices", srv.listDevices)
	protected("POST /api/devices", srv.addDevice)
	protected("POST /api/devices/import", srv.importCSV)
	protected("DELETE /api/devices/{mac}", srv.deleteDevice)
	protected("PATCH /api/devices/{mac}/status", srv.updateDeviceStatus)

	protected("GET /api/admin-users", srv.listAdminUsers)
	protected("POST /api/admin-users", srv.createAdminUser)
	protected("DELETE /api/admin-users/{username}", srv.deleteAdminUser)

	protected("GET /api/tenant-keys", srv.listTenantKeys)
	protected("POST /api/tenant-keys/{tid}/generate", srv.generateTenantKey)

	// Released products + inventory — proxied to the cloud /admin/* APIs with the
	// service token attached server-side (the browser only holds the session cookie).
	protected("GET /api/released-products", srv.proxyReleasedProducts)
	protected("POST /api/released-products/retire", srv.retireReleasedProduct)
	protected("GET /api/inventory", srv.proxyInventory)
	protected("GET /api/inventory/{did}/ztp", srv.proxyDeviceZTP)
	protected("GET /api/batches", srv.proxyBatches)
	protected("POST /api/inventory/batches", srv.createInventoryBatch)

	log.Printf("setu admin → http://localhost%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

// ── Auth middleware ───────────────────────────────────────────────────────────

func (s *server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.checkSession(r) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) checkSession(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	s.mu.RLock()
	sess, ok := s.sessions[cookie.Value]
	s.mu.RUnlock()
	return ok && time.Now().Before(sess.ExpiresAt)
}

func (s *server) sessionUser(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	s.mu.RLock()
	sess := s.sessions[cookie.Value]
	s.mu.RUnlock()
	return sess.Username
}

// ── Auth handlers ─────────────────────────────────────────────────────────────

func (s *server) serveLogin(w http.ResponseWriter, r *http.Request) {
	if s.checkSession(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	data, _ := uiFS.ReadFile("ui/login.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	var userID, hash string
	err := s.db.QueryRow(r.Context(),
		`SELECT id, password_hash FROM admin_users WHERE username=$1`, username,
	).Scan(&userID, &hash)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		http.Redirect(w, r, "/login?error=1", http.StatusFound)
		return
	}

	token := genSecret(32)
	s.mu.Lock()
	s.sessions[token] = sessionData{
		UserID:    userID,
		Username:  username,
		ExpiresAt: time.Now().Add(sessionTTL),
	}
	s.mu.Unlock()

	s.db.Exec(r.Context(), `UPDATE admin_users SET last_login_at=NOW() WHERE id=$1`, userID)

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		s.mu.Lock()
		delete(s.sessions, cookie.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (s *server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"username": s.sessionUser(r)})
}

// ── Admin user management ─────────────────────────────────────────────────────

func (s *server) listAdminUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(),
		`SELECT id, username, created_at, last_login_at FROM admin_users ORDER BY created_at`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()

	out := []adminUser{}
	for rows.Next() {
		var u adminUser
		if err := rows.Scan(&u.ID, &u.Username, &u.CreatedAt, &u.LastLoginAt); err == nil {
			out = append(out, u)
		}
	}
	writeJSON(w, 200, out)
}

func (s *server) createAdminUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		writeErr(w, 400, "username required")
		return
	}
	if len(req.Password) < 8 {
		writeErr(w, 400, "password must be at least 8 characters")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, "hash error")
		return
	}

	var id = uuid.New().String()
	_, err = s.db.Exec(r.Context(),
		`INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, $3)`,
		id, req.Username, string(hash))
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			writeErr(w, 409, "username already exists")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]string{"id": id, "username": req.Username})
}

func (s *server) deleteAdminUser(w http.ResponseWriter, r *http.Request) {
	target := r.PathValue("username")
	if target == s.sessionUser(r) {
		writeErr(w, 409, "cannot delete your own account")
		return
	}

	ct, err := s.db.Exec(r.Context(),
		`DELETE FROM admin_users WHERE username=$1`, target)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if ct.RowsAffected() == 0 {
		writeErr(w, 404, "user not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Device inventory ──────────────────────────────────────────────────────────

func (s *server) listDevices(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `
		SELECT di.mac, di.tid, di.did, di.pid, COALESCE(t.mq_user, ''), di.hw_config,
		       di.registered_at, di.provisioned_at
		FROM device_inventory di JOIN tenants t ON t.tid = di.tid
		ORDER BY di.registered_at DESC`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()

	out := []device{}
	for rows.Next() {
		var d device
		if err := rows.Scan(&d.MAC, &d.TID, &d.DID, &d.PID, &d.MQUser,
			&d.HWConfig, &d.RegisteredAt, &d.ProvisionedAt); err == nil {
			out = append(out, d)
		}
	}
	writeJSON(w, 200, out)
}

func (s *server) addDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MAC      string          `json:"mac"`
		TID      string          `json:"tid"`
		PID      string          `json:"pid"`
		HWConfig json.RawMessage `json:"hw_config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	mac := normMAC(req.MAC)
	if !macRegex.MatchString(mac) {
		writeErr(w, 400, "invalid MAC address — must be 12 hex characters")
		return
	}
	if req.PID == "" {
		writeErr(w, 400, "pid required")
		return
	}
	tid := req.TID
	if tid == "" {
		tid = s.tid
	}
	hwConfig := req.HWConfig
	if len(hwConfig) == 0 {
		hwConfig = json.RawMessage(`{}`)
	}

	did := uuid.New().String()
	// Ensure the tenant has its single shared MQTT credential (mq_user = tid).
	// Devices share it; identity at runtime comes from clientid = did.
	var mqUser, mqPass string
	if err := s.db.QueryRow(r.Context(), `
		INSERT INTO tenants (tid, name, api_key_hash, mq_user, mq_pass)
		VALUES ($1, $1, $2, $1, $3)
		ON CONFLICT (tid) DO UPDATE
		  SET mq_user = COALESCE(tenants.mq_user, EXCLUDED.mq_user),
		      mq_pass = COALESCE(tenants.mq_pass, EXCLUDED.mq_pass)
		RETURNING mq_user, mq_pass`,
		tid, "seeded:"+genSecret(8), genSecret(16)).Scan(&mqUser, &mqPass); err != nil {
		writeErr(w, 500, "could not resolve tenant mqtt credential: "+err.Error())
		return
	}

	_, err := s.db.Exec(r.Context(), `
		INSERT INTO device_inventory (mac, tid, did, pid, hw_config)
		VALUES ($1, $2, $3, $4, $5)`,
		mac, tid, did, req.PID, hwConfig)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			writeErr(w, 409, "MAC address already exists in inventory")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}

	emqxStatus := "skipped (EMQX credentials not configured)"
	if s.emqxKey != "" {
		if err := s.createEMQXUser(mqUser, mqPass); err != nil {
			emqxStatus = "error: " + err.Error()
		} else {
			emqxStatus = "created"
		}
	}

	writeJSON(w, 201, map[string]any{
		"mac": mac, "did": did,
		"mq_user": mqUser, "mq_pass": mqPass,
		"emqx_status": emqxStatus,
	})
}

func (s *server) importCSV(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		writeErr(w, 400, "could not parse form")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "file field required")
		return
	}
	defer file.Close()

	cr := csv.NewReader(file)
	cr.TrimLeadingSpace = true
	records, err := cr.ReadAll()
	if err != nil {
		writeErr(w, 400, "invalid CSV: "+err.Error())
		return
	}
	if len(records) < 2 {
		writeErr(w, 400, "CSV must have a header row and at least one data row")
		return
	}

	header := map[string]int{}
	for i, h := range records[0] {
		header[strings.ToLower(strings.TrimSpace(h))] = i
	}
	macIdx, ok := header["mac"]
	if !ok {
		writeErr(w, 400, "CSV missing required 'mac' column")
		return
	}
	pidIdx, hasPID := header["pid"]
	hwIdx, hasHW := header["hw_config"]
	tidIdx, hasTID := header["tid"]

	type result struct {
		MAC        string `json:"mac"`
		DID        string `json:"did,omitempty"`
		MQUser     string `json:"mq_user,omitempty"`
		MQPass     string `json:"mq_pass,omitempty"`
		EMQXStatus string `json:"emqx_status,omitempty"`
		Status     string `json:"status"`
		Error      string `json:"error,omitempty"`
	}

	var results []result
	for i, rec := range records[1:] {
		if len(rec) <= macIdx {
			continue
		}
		mac := normMAC(rec[macIdx])
		if !macRegex.MatchString(mac) {
			results = append(results, result{MAC: rec[macIdx], Status: "error", Error: "invalid MAC"})
			continue
		}

		pid := ""
		if hasPID && len(rec) > pidIdx {
			pid = strings.TrimSpace(rec[pidIdx])
		}
		if pid == "" {
			results = append(results, result{MAC: mac, Status: "error",
				Error: fmt.Sprintf("row %d: pid required", i+2)})
			continue
		}

		hwConfig := json.RawMessage(`{}`)
		if hasHW && len(rec) > hwIdx && strings.TrimSpace(rec[hwIdx]) != "" {
			hwConfig = json.RawMessage(strings.TrimSpace(rec[hwIdx]))
		}

		tid := s.tid
		if hasTID && len(rec) > tidIdx && strings.TrimSpace(rec[tidIdx]) != "" {
			tid = strings.TrimSpace(rec[tidIdx])
		}

		did := uuid.New().String()
		// Shared per-tenant MQTT credential (mq_user = tid), idempotent per tid.
		var mqUser, mqPass string
		if err := s.db.QueryRow(r.Context(), `
			INSERT INTO tenants (tid, name, api_key_hash, mq_user, mq_pass)
			VALUES ($1, $1, $2, $1, $3)
			ON CONFLICT (tid) DO UPDATE
			  SET mq_user = COALESCE(tenants.mq_user, EXCLUDED.mq_user),
			      mq_pass = COALESCE(tenants.mq_pass, EXCLUDED.mq_pass)
			RETURNING mq_user, mq_pass`,
			tid, "seeded:"+genSecret(8), genSecret(16)).Scan(&mqUser, &mqPass); err != nil {
			results = append(results, result{MAC: mac, Status: "error", Error: "tenant cred: " + err.Error()})
			continue
		}

		_, err := s.db.Exec(r.Context(), `
			INSERT INTO device_inventory (mac, tid, did, pid, hw_config)
			VALUES ($1, $2, $3, $4, $5) ON CONFLICT (mac) DO NOTHING`,
			mac, tid, did, pid, hwConfig)
		if err != nil {
			results = append(results, result{MAC: mac, Status: "error", Error: err.Error()})
			continue
		}

		emqxStatus := "skipped"
		if s.emqxKey != "" {
			if err := s.createEMQXUser(mqUser, mqPass); err != nil {
				emqxStatus = "error: " + err.Error()
			} else {
				emqxStatus = "created"
			}
		}
		results = append(results, result{
			MAC: mac, DID: did, MQUser: mqUser, MQPass: mqPass,
			EMQXStatus: emqxStatus, Status: "ok",
		})
	}
	writeJSON(w, 200, map[string]any{"results": results})
}

func (s *server) updateDeviceStatus(w http.ResponseWriter, r *http.Request) {
	mac := normMAC(r.PathValue("mac"))

	var req struct {
		Provisioned bool `json:"provisioned"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}

	var ct interface{ RowsAffected() int64 }
	var err error

	if req.Provisioned {
		// Mark as provisioned — also upsert into devices table so the platform
		// accepts MQTT messages from this device.
		tx, txErr := s.db.Begin(r.Context())
		if txErr != nil {
			writeErr(w, 500, txErr.Error())
			return
		}
		defer tx.Rollback(r.Context())

		res, execErr := tx.Exec(r.Context(),
			`UPDATE device_inventory SET provisioned_at=NOW() WHERE mac=$1 AND provisioned_at IS NULL`,
			mac)
		if execErr != nil {
			writeErr(w, 500, execErr.Error())
			return
		}
		if res.RowsAffected() == 0 {
			writeErr(w, 404, "device not found or already provisioned")
			return
		}

		// Ensure a devices row exists so the device can connect via MQTT.
		var tid, did, pid string
		tx.QueryRow(r.Context(),
			`SELECT tid, did, pid FROM device_inventory WHERE mac=$1`, mac,
		).Scan(&tid, &did, &pid)
		tx.Exec(r.Context(),
			`INSERT INTO devices (tid, did, pid, is_online, registered_at)
			 VALUES ($1, $2, $3, false, NOW())
			 ON CONFLICT (tid, did) DO NOTHING`,
			tid, did, pid)

		if err = tx.Commit(r.Context()); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"mac": mac, "status": "provisioned"})
	} else {
		// Reset to pending — clears provisioned_at so the device can be
		// re-provisioned via ZTP (factory reset scenario).
		ct, err = s.db.Exec(r.Context(),
			`UPDATE device_inventory SET provisioned_at=NULL WHERE mac=$1`, mac)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if ct.RowsAffected() == 0 {
			writeErr(w, 404, "device not found")
			return
		}
		writeJSON(w, 200, map[string]string{"mac": mac, "status": "pending"})
	}
}

func (s *server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	mac := normMAC(r.PathValue("mac"))

	var provisioned bool
	s.db.QueryRow(r.Context(),
		`SELECT provisioned_at IS NOT NULL FROM device_inventory WHERE mac=$1`, mac,
	).Scan(&provisioned)
	if provisioned {
		writeErr(w, 409, "cannot delete a provisioned device")
		return
	}

	ct, err := s.db.Exec(r.Context(),
		`DELETE FROM device_inventory WHERE mac=$1 AND provisioned_at IS NULL`, mac)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if ct.RowsAffected() == 0 {
		writeErr(w, 404, "device not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Tenant key management ─────────────────────────────────────────────────────

func (s *server) listTenantKeys(w http.ResponseWriter, r *http.Request) {
	if s.ks == nil {
		writeErr(w, 503, "KEY_ENCRYPTION_KEY not configured — tenant key management unavailable")
		return
	}
	keys, err := s.ks.ListKeyInfo(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if keys == nil {
		keys = []keystore.KeyInfo{}
	}
	writeJSON(w, 200, keys)
}

func (s *server) generateTenantKey(w http.ResponseWriter, r *http.Request) {
	if s.ks == nil {
		writeErr(w, 503, "KEY_ENCRYPTION_KEY not configured — tenant key management unavailable")
		return
	}
	tid := r.PathValue("tid")
	if tid == "" {
		writeErr(w, 400, "tid required")
		return
	}

	// Verify tenant exists.
	var exists bool
	s.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM tenants WHERE tid=$1)`, tid).Scan(&exists)
	if !exists {
		writeErr(w, 404, "tenant not found")
		return
	}

	tk, err := s.ks.Generate(r.Context(), tid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{
		"key_id":      tk.KeyID,
		"tid":         tk.TID,
		"pub_key_hex": tk.PubKeyHex,
		"created_at":  tk.CreatedAt,
	})
}

// ── Cloud /admin/* proxy ─────────────────────────────────────────────────────
//
// These handlers forward to the setu-cloud API's service-token-guarded /admin/*
// endpoints. The token is attached here, server-side — it is never sent to the
// browser, which authenticates to this admin portal with its session cookie only.

// cloudRequest issues an authenticated request to the cloud /admin/* API.
func (s *server) cloudRequest(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, s.cloudBase+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.svcToken != "" {
		req.Header.Set("X-Service-Token", s.svcToken)
		req.Header.Set("Authorization", "Bearer "+s.svcToken)
	}
	return (&http.Client{Timeout: 15 * time.Second}).Do(req)
}

// relayCloud streams the cloud response's status code + JSON body verbatim back
// to the browser, so the front-end api() wrapper sees the cloud's errors as-is.
func (s *server) relayCloud(w http.ResponseWriter, resp *http.Response, err error) {
	if err != nil {
		writeErr(w, http.StatusBadGateway, "cloud unreachable: "+err.Error())
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (s *server) proxyGet(w http.ResponseWriter, r *http.Request, path string) {
	resp, err := s.cloudRequest(r.Context(), http.MethodGet, path, nil)
	s.relayCloud(w, resp, err)
}

func (s *server) proxyPost(w http.ResponseWriter, r *http.Request, path string, body []byte) {
	resp, err := s.cloudRequest(r.Context(), http.MethodPost, path, bytes.NewReader(body))
	s.relayCloud(w, resp, err)
}

func (s *server) proxyReleasedProducts(w http.ResponseWriter, r *http.Request) {
	s.proxyGet(w, r, "/admin/released-products?"+r.URL.RawQuery)
}

func (s *server) proxyInventory(w http.ResponseWriter, r *http.Request) {
	s.proxyGet(w, r, "/admin/inventory?"+r.URL.RawQuery)
}

func (s *server) proxyBatches(w http.ResponseWriter, r *http.Request) {
	s.proxyGet(w, r, "/admin/batches?"+r.URL.RawQuery)
}

// retireReleasedProduct forwards a staff retire/restore toggle to the cloud.
func (s *server) retireReleasedProduct(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.proxyPost(w, r, "/admin/released-products/retire", body)
}

// proxyDeviceZTP forwards a read-only ZTP provision-bundle preview request.
func (s *server) proxyDeviceZTP(w http.ResponseWriter, r *http.Request) {
	s.proxyGet(w, r, "/admin/inventory/"+r.PathValue("did")+"/ztp")
}

// createInventoryBatch forwards a batch-creation request, stamping created_by
// with the signed-in staff username. The body is JSON {tid,pid,schema_version,
// macs[]|qty,integrator_note}; CSV is already parsed to macs[] client-side.
func (s *server) createInventoryBatch(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req["created_by"] = s.sessionUser(r)
	body, _ := json.Marshal(req)
	s.proxyPost(w, r, "/admin/inventory/batches", body)
}

// ── EMQX ─────────────────────────────────────────────────────────────────────

func (s *server) createEMQXUser(userID, password string) error {
	body, _ := json.Marshal(map[string]string{"user_id": userID, "password": password})
	req, _ := http.NewRequest(http.MethodPost,
		s.emqxBase+"/api/v5/authentication/password_based:built_in_database/users",
		bytes.NewReader(body))
	req.SetBasicAuth(s.emqxKey, s.emqxSecret)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return nil // shared per-tenant user already exists — idempotent
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("EMQX %d: %s", resp.StatusCode, b)
	}
	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func normMAC(s string) string {
	return strings.ToLower(strings.NewReplacer(":", "", "-", "").Replace(strings.TrimSpace(s)))
}

func genSecret(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required env var %s not set", key)
	}
	return v
}
