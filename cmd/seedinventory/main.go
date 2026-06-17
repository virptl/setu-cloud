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

	var results []result
	for _, r := range rows {
		res := seed(ctx, pool, tid, r, emqxBase, emqxKey, emqxSecret)
		results = append(results, res)
		log.Printf("[%s] mac=%-12s did=%s status=%s", res.Status, res.MAC, res.DID, res.Status)
	}

	// Print summary CSV to stdout.
	fmt.Println("\nmac,did,mq_user,mq_pass,status")
	for _, r := range results {
		fmt.Printf("%s,%s,%s,%s,%s\n", r.MAC, r.DID, r.MQUser, r.MQPass, r.Status)
	}

	if emqxKey == "" {
		fmt.Println("\n⚠  EMQX_API_KEY not set — EMQX users were NOT created.")
		fmt.Println("Run these curl commands manually (replace <key>:<secret> with your EMQX API key):")
		for _, r := range results {
			if r.Status == "ok" {
				fmt.Printf("curl -s -u <key>:<secret> -X POST %s/api/v5/authentication/password_based:built_in_database/users -H 'content-type: application/json' -d '{\"user_id\":\"%s\",\"password\":\"%s\"}'\n",
					emqxBase, r.MQUser, r.MQPass)
			}
		}
	}
}

func seed(ctx context.Context, pool *pgxpool.Pool, tid string, r row, emqxBase, emqxKey, emqxSecret string) result {
	did := uuid.New().String()
	mqUser := fmt.Sprintf("%s.%s", tid, did)
	mqPass := genSecret(16)

	_, err := pool.Exec(ctx, `
		INSERT INTO device_inventory (mac, tid, did, pid, mq_user, mq_pass, hw_config)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (mac) DO NOTHING
	`, r.MAC, tid, did, r.PID, mqUser, mqPass, r.HWConfig)
	if err != nil {
		return result{MAC: r.MAC, Status: "db_error: " + err.Error()}
	}

	// Re-read in case ON CONFLICT skipped the insert (already exists).
	var existingDID, existingUser, existingPass string
	pool.QueryRow(ctx,
		`SELECT did, mq_user, mq_pass FROM device_inventory WHERE mac=$1`, r.MAC,
	).Scan(&existingDID, &existingUser, &existingPass)
	did, mqUser, mqPass = existingDID, existingUser, existingPass

	res := result{MAC: r.MAC, DID: did, MQUser: mqUser, MQPass: mqPass, Status: "ok"}

	if emqxKey != "" {
		if err := createEMQXUser(emqxBase, emqxKey, emqxSecret, mqUser, mqPass); err != nil {
			res.Status = "emqx_error: " + err.Error()
		}
	}

	return res
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
