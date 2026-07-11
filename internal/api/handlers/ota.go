package handlers

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/setucore/setu-cloud/internal/keystore"
	"github.com/setucore/setu-cloud/internal/mqtt"
)

// AdminIssueOTA signs a firmware image with the device's tenant cloud key and
// publishes a signed "ota" /dn command.
//
//	POST /admin/devices/{did}/ota
//	body: {"url":"https://…/firmware.uf2","epoch":<int>,"sha256":"<hex>"?}
//
// The signature covers SHA-256(uf2)||epoch_be32 (matches the firmware). If
// sha256 is omitted the server fetches the URL and computes it — only works when
// the URL is reachable from the cloud (bench images on a private LAN must pass
// sha256 explicitly). epoch must be >= the device's running SETU_FW_EPOCH or the
// firmware rejects it (anti-rollback).
func AdminIssueOTA(db *pgxpool.Pool, pub *mqtt.Publisher, ks *keystore.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		did := chi.URLParam(r, "did")
		var body struct {
			URL    string `json:"url"`
			Epoch  uint32 `json:"epoch"`
			SHA256 string `json:"sha256"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.URL == "" {
			adminErr(w, http.StatusBadRequest, "bad_request: url required")
			return
		}

		var tid, pid string
		if err := db.QueryRow(r.Context(),
			`SELECT tid, pid FROM devices WHERE did=$1`, did).Scan(&tid, &pid); err != nil {
			adminErr(w, http.StatusNotFound, "device not found")
			return
		}

		var hash []byte
		if body.SHA256 != "" {
			h, err := hex.DecodeString(body.SHA256)
			if err != nil || len(h) != 32 {
				adminErr(w, http.StatusBadRequest, "bad sha256 (want 64 hex)")
				return
			}
			hash = h
		} else {
			h, err := hashURL(r.Context(), body.URL)
			if err != nil {
				adminErr(w, http.StatusBadGateway, "fetch/hash failed: "+err.Error())
				return
			}
			hash = h
		}

		// sign over SHA-256( sha256(uf2) || epoch_be32 )
		msg := make([]byte, 36)
		copy(msg, hash)
		binary.BigEndian.PutUint32(msg[32:], body.Epoch)
		digest := sha256.Sum256(msg)

		tk, err := ks.ActiveKey(r.Context(), tid)
		if err != nil {
			adminErr(w, http.StatusInternalServerError, "no signing key for tenant: "+err.Error())
			return
		}
		rr, ss, err := ecdsa.Sign(rand.Reader, tk.PrivKey, digest[:])
		if err != nil {
			adminErr(w, http.StatusInternalServerError, "sign failed")
			return
		}
		sig := make([]byte, 64)
		rr.FillBytes(sig[0:32])
		ss.FillBytes(sig[32:64])
		sigHex := hex.EncodeToString(sig)

		d, _ := json.Marshal(map[string]any{"url": body.URL, "sig": sigHex, "epoch": body.Epoch})
		cmdID := uuid.New().String()

		// Persist as pending so the device's ack updates this row (like other
		// commands). FK (tid,did) -> devices is satisfied by the lookup above.
		if _, err := db.Exec(r.Context(), `
			INSERT INTO commands (id, tid, did, command_type, payload, status, issued_at)
			VALUES ($1, $2, $3, 'ota', $4, 'pending', NOW())
		`, cmdID, tid, did, d); err != nil {
			adminErr(w, http.StatusInternalServerError, "persist command: "+err.Error())
			return
		}

		if err := pub.Publish(tid, pid, did, "ota", cmdID, d); err != nil {
			adminErr(w, http.StatusBadGateway, "publish failed: "+err.Error())
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"published": true,
			"did":       did,
			"tid":       tid,
			"pid":       pid,
			"cmd_id":    cmdID,
			"sha256":    hex.EncodeToString(hash),
			"epoch":     body.Epoch,
			"sig":       sigHex,
		})
	}
}

func hashURL(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	h := sha256.New()
	if _, err := io.Copy(h, resp.Body); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}
