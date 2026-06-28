// seedinventory seeds device_inventory from a CSV file and creates matching
// EMQX users via the EMQX HTTP API.
//
// CSV format (header required):
//   mac,pid,hw_config
//   aabbccddeeff,light1,{}
//   112233445566,sp1,{"relay_pin":5}
//
// Usage:
//   seedinventory -csv devices.csv
//
// Env vars:
//   DATABASE_URL         — Postgres connection string (required)
//   CONSUMER_TID         — tenant ID (default: setu)
//   EMQX_API_URL         — EMQX HTTP API base (default: http://localhost:18083)
//   EMQX_API_KEY         — EMQX API key (optional; skip EMQX if absent)
//   EMQX_API_SECRET      — EMQX API secret (optional)

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

var macRegex = regexp.MustCompile(`^[0-9a-f]{12}$`)

type row struct {
	MAC      string
	PID      string
	HWConfig string
}

type result struct {
	MAC    string
	DID    string
	MQUser string
	MQPass string
	Status string
}

func main() {
	csvFile := flag.String("csv", "", "path to input CSV file (required)")
	flag.Parse()

	if *csvFile == "" {
		log.Fatal("usage: seedinventory -csv <file.csv>")
	}

	dbURL := mustEnv("DATABASE_URL")
	tid := env("CONSUMER_TID", "setu")
	emqxBase := env("EMQX_API_URL", "http://localhost:18083")
	emqxKey := os.Getenv("EMQX_API_KEY")
	emqxSecret := os.Getenv("EMQX_API_SECRET")

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("db connect: %v", err)
	}
	defer pool.Close()

	rows, err := parseCSV(*csvFile)
	if err != nil {
		log.Fatalf("csv: %v", err)
	}
	log.Printf("loaded %d devices from %s", len(rows), *csvFile)

	// MQTT credentials are shared per tenant: ensure the tenant has a single
	// (mq_user = tid, mq_pass) pair + one EMQX user, then reuse it for all devices.
	mqUser, mqPass, created, err := ensureTenantMQTT(ctx, pool, tid, emqxBase, emqxKey, emqxSecret)
	if err != nil {
		log.Fatalf("ensure tenant mqtt credential for tid=%s: %v", tid, err)
	}
	if created {
		log.Printf("tenant %s: minted shared MQTT credential mq_user=%s", tid, mqUser)
	} else {
		log.Printf("tenant %s: reusing existing shared MQTT credential mq_user=%s", tid, mqUser)
	}

	var results []result
	for _, r := range rows {
		res := seed(ctx, pool, tid, r)
		res.MQUser, res.MQPass = mqUser, mqPass
		results = append(results, res)
		log.Printf("[%s] mac=%-12s did=%s status=%s", res.Status, res.MAC, res.DID, res.Status)
	}

	// Print summary CSV to stdout. mq_user/mq_pass are the tenant-shared values.
	fmt.Println("\nmac,did,mq_user,mq_pass,status")
	for _, r := range results {
		fmt.Printf("%s,%s,%s,%s,%s\n", r.MAC, r.DID, r.MQUser, r.MQPass, r.Status)
	}

	if emqxKey == "" {
		fmt.Println("\n⚠  EMQX_API_KEY not set — the tenant EMQX user was NOT created.")
		fmt.Println("Run this once for the tenant (replace <key>:<secret> with your EMQX API key):")
		fmt.Printf("curl -s -u <key>:<secret> -X POST %s/api/v5/authentication/password_based:built_in_database/users -H 'content-type: application/json' -d '{\"user_id\":\"%s\",\"password\":\"%s\"}'\n",
			emqxBase, mqUser, mqPass)
	}
}

// ensureTenantMQTT returns the tenant's shared MQTT credential, minting one
// (mq_user = tid, random mq_pass) and creating the matching EMQX user on first
// call. created reports whether a new credential was generated this run.
func ensureTenantMQTT(ctx context.Context, pool *pgxpool.Pool, tid, emqxBase, emqxKey, emqxSecret string) (mqUser, mqPass string, created bool, err error) {
	var existingUser, existingPass *string
	if err = pool.QueryRow(ctx,
		`SELECT mq_user, mq_pass FROM tenants WHERE tid=$1`, tid,
	).Scan(&existingUser, &existingPass); err != nil {
		return "", "", false, err
	}
	if existingUser != nil && *existingUser != "" && existingPass != nil && *existingPass != "" {
		return *existingUser, *existingPass, false, nil
	}

	mqUser = tid
	mqPass = genSecret(16)
	if _, err = pool.Exec(ctx,
		`UPDATE tenants SET mq_user=$2, mq_pass=$3 WHERE tid=$1`, tid, mqUser, mqPass,
	); err != nil {
		return "", "", false, err
	}
	if emqxKey != "" {
		if err = createEMQXUser(emqxBase, emqxKey, emqxSecret, mqUser, mqPass); err != nil {
			return "", "", false, fmt.Errorf("emqx: %w", err)
		}
	}
	return mqUser, mqPass, true, nil
}

// seed upserts one device_inventory row. The device's identity is its did (used
// as the MQTT clientid); MQTT credentials are shared per tenant, not per device.
func seed(ctx context.Context, pool *pgxpool.Pool, tid string, r row) result {
	did := uuid.New().String()
	bleMac := macPlusN(r.MAC, 2)

	_, err := pool.Exec(ctx, `
		INSERT INTO device_inventory (mac, ble_mac, tid, did, pid, hw_config)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (mac) DO UPDATE SET ble_mac = EXCLUDED.ble_mac
	`, r.MAC, bleMac, tid, did, r.PID, r.HWConfig)
	if err != nil {
		return result{MAC: r.MAC, Status: "db_error: " + err.Error()}
	}

	// Re-read in case ON CONFLICT skipped the insert (device already existed).
	var existingDID string
	pool.QueryRow(ctx,
		`SELECT did FROM device_inventory WHERE mac=$1`, r.MAC,
	).Scan(&existingDID)

	return result{MAC: r.MAC, DID: existingDID, Status: "ok"}
}

func createEMQXUser(base, key, secret, userID, password string) error {
	body, _ := json.Marshal(map[string]string{"user_id": userID, "password": password})
	req, _ := http.NewRequest(http.MethodPost,
		base+"/api/v5/authentication/password_based:built_in_database/users",
		bytes.NewReader(body))
	req.SetBasicAuth(key, secret)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return nil // shared per-tenant user already exists — idempotent
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("EMQX returned %d", resp.StatusCode)
	}
	return nil
}

func parseCSV(path string) ([]row, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.TrimLeadingSpace = true
	records, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(records) < 2 {
		return nil, fmt.Errorf("CSV must have a header row and at least one data row")
	}

	// Map header columns.
	header := map[string]int{}
	for i, h := range records[0] {
		header[strings.ToLower(strings.TrimSpace(h))] = i
	}
	macIdx, ok := header["mac"]
	if !ok {
		return nil, fmt.Errorf("CSV missing 'mac' column")
	}
	pidIdx, hasPID := header["pid"]
	hwIdx, hasHW := header["hw_config"]

	var rows []row
	for i, rec := range records[1:] {
		mac := strings.ToLower(strings.NewReplacer(":", "", "-", "").Replace(strings.TrimSpace(rec[macIdx])))
		if !macRegex.MatchString(mac) {
			return nil, fmt.Errorf("row %d: invalid mac %q", i+2, mac)
		}
		r := row{MAC: mac, HWConfig: "{}"}
		if hasPID {
			r.PID = strings.TrimSpace(rec[pidIdx])
		}
		if hasHW && strings.TrimSpace(rec[hwIdx]) != "" {
			r.HWConfig = strings.TrimSpace(rec[hwIdx])
		}
		if r.PID == "" {
			return nil, fmt.Errorf("row %d: pid is required", i+2)
		}
		rows = append(rows, r)
	}
	return rows, nil
}

// macPlusN increments a 12-char lowercase hex MAC by n.
// ESP32 assigns BLE MAC = Wi-Fi base MAC + 2.
func macPlusN(mac string, n uint64) string {
	b, _ := hex.DecodeString(mac)
	// Pad to 8 bytes for uint64 arithmetic.
	padded := make([]byte, 8)
	copy(padded[8-len(b):], b)
	val := binary.BigEndian.Uint64(padded) + n
	binary.BigEndian.PutUint64(padded, val)
	return hex.EncodeToString(padded[2:]) // back to 6 bytes = 12 hex chars
}

func genSecret(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
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
