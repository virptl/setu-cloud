package ztp

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/setucore/setu-cloud/internal/config"
	"github.com/setucore/setu-cloud/internal/keystore"
)

var macRegex = regexp.MustCompile(`^[0-9a-f]{12}$`)

type provisionReq struct {
	MAC string `json:"mac"`
}

type provisionResp struct {
	DID         string          `json:"did"`
	TID         string          `json:"tid"`
	PID         string          `json:"pid"`
	MQUser      string          `json:"mq_user"`
	MQPass      string          `json:"mq_pass"`
	MQURI       string          `json:"mq_uri"`
	CloudPubkey string          `json:"cloud_pubkey"`
	HWConfig    json.RawMessage `json:"hw_config"`
}

// HandleProvision serves POST /factory/provision.
func HandleProvision(db *pgxpool.Pool, cfg *config.Config, ks *keystore.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cfg.FactoryProvToken != "" {
			if r.Header.Get("X-Factory-Token") != cfg.FactoryProvToken {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
		}

		var req provisionReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"bad_request","msg":"invalid json"}`, http.StatusBadRequest)
			return
		}

		req.MAC = strings.ToLower(strings.NewReplacer(":", "", "-", "").Replace(req.MAC))
		if !macRegex.MatchString(req.MAC) {
			http.Error(w, `{"error":"bad_request","msg":"invalid mac"}`, http.StatusBadRequest)
			return
		}

		entry, err := claimDevice(r.Context(), db, req.MAC)
		switch {
		case errors.Is(err, ErrNotFound):
			http.Error(w, `{"error":"not_found","msg":"device not in inventory"}`, http.StatusNotFound)
			return
		case errors.Is(err, ErrAlreadyProvisioned):
			http.Error(w, `{"error":"conflict","msg":"already provisioned"}`, http.StatusConflict)
			return
		case err != nil:
			log.Printf("ztp: claimDevice mac=%s: %v", req.MAC, err)
			http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
			return
		}

		// Look up the active cloud public key for this device's tenant.
		// Falls back to the legacy global env var if keystore has no entry yet.
		cloudPubkey, err := ks.ActivePubKey(r.Context(), entry.TID)
		if err != nil {
			log.Printf("ztp: keystore ActivePubKey tid=%s: %v — falling back to cfg", entry.TID, err)
			cloudPubkey = cfg.CloudPubkeyHex
		}

		resp := provisionResp{
			DID:         entry.DID,
			TID:         entry.TID,
			PID:         entry.PID,
			MQUser:      entry.MQUser,
			MQPass:      entry.MQPass,
			MQURI:       cfg.DeviceMQTTBrokerURI,
			CloudPubkey: cloudPubkey,
			HWConfig:    entry.HWConfig,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}
