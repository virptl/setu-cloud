// Package proactive subscribes to Redis device events and proactively pushes
// state changes to Alexa (ChangeReport) and Google Home (Report State).
package proactive

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"

	"github.com/redis/go-redis/v9"

	"github.com/setucore/setu-cloud/internal/config"
	"github.com/setucore/setu-cloud/internal/iot"
	"github.com/setucore/setu-cloud/internal/oauth"
)

// wsEvent mirrors the structure published by mqtt.Router.publishWS.
type wsEvent struct {
	Type string          `json:"type"`
	TID  string          `json:"tid"`
	DID  string          `json:"did"`
	T    int64           `json:"t"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Service listens on ws:events:* and fans out state changes to linked voice platforms.
type Service struct {
	cache      *redis.Client
	oauthStore *oauth.Store
	iotSvc     *iot.Service
	cfg        *config.Config
}

func New(cache *redis.Client, oauthStore *oauth.Store, iotSvc *iot.Service, cfg *config.Config) *Service {
	return &Service{cache: cache, oauthStore: oauthStore, iotSvc: iotSvc, cfg: cfg}
}

// Run subscribes to ws:events:* and blocks until ctx is cancelled.
// Call as a goroutine from main.
func (s *Service) Run(ctx context.Context) {
	psub := s.cache.PSubscribe(ctx, "ws:events:*")
	defer psub.Close()

	ch := psub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			go s.handleEvent(ctx, msg.Payload)
		}
	}
}

// TriggerDiscoveryRefresh asks Alexa and Google to re-discover a user's devices.
// Called after a new device is claimed or adopted. Fire-and-forget.
func (s *Service) TriggerDiscoveryRefresh(userID string) {
	ctx := context.Background()
	platforms, err := s.oauthStore.LinkedPlatforms(ctx, userID)
	if err != nil {
		return
	}
	saToken := s.cfg.GoogleSAToken
	for _, p := range platforms {
		switch p.Platform {
		case "alexa":
			if p.AlexaBearerToken != "" {
				go s.pushAlexaDiscoveryRefresh(ctx, userID, p.AlexaBearerToken)
			}
		case "google":
			if saToken != "" {
				go s.pushGoogleRequestSync(ctx, userID, saToken)
			}
		}
	}
}

func (s *Service) handleEvent(ctx context.Context, rawMsg string) {
	var ev wsEvent
	if err := json.Unmarshal([]byte(rawMsg), &ev); err != nil {
		return
	}
	if ev.Type != "rpt" {
		return
	}

	ownerID, err := s.oauthStore.OwnerOfDevice(ctx, ev.DID)
	if err != nil {
		return
	}

	_, pid, ok := s.iotSvc.OwnsDevice(ctx, ownerID, ev.DID)
	if !ok {
		return
	}

	dps := s.iotSvc.GetReportedDPS(ctx, s.cfg.ConsumerTID, ev.DID)
	if len(dps) == 0 {
		return
	}

	platforms, err := s.oauthStore.LinkedPlatforms(ctx, ownerID)
	if err != nil || len(platforms) == 0 {
		return
	}

	saToken := s.cfg.GoogleSAToken
	for _, p := range platforms {
		switch p.Platform {
		case "alexa":
			if _, _, ok := s.iotSvc.OwnsDeviceForAssistant(ctx, ownerID, ev.DID, "alexa"); ok {
				pushAlexaChangeReport(ctx, ev.DID, pid, p.AlexaBearerToken, dps)
			}
		case "google":
			if saToken != "" {
				if _, _, ok := s.iotSvc.OwnsDeviceForAssistant(ctx, ownerID, ev.DID, "google"); ok {
					pushGoogleReportState(ctx, ev.DID, pid, ownerID, saToken, dps)
				}
			}
		}
	}
}

// pushAlexaDiscoveryRefresh logs the intent; full AddOrUpdateReport is out of scope for MVP.
// Alexa will re-discover automatically when the user opens the Alexa app.
func (s *Service) pushAlexaDiscoveryRefresh(ctx context.Context, userID, _ string) {
	devices, _ := s.iotSvc.ListDevicesForAssistant(ctx, userID, "alexa")
	log.Printf("alexa discovery refresh: user=%s devices=%d", userID, len(devices))
}

// pushGoogleRequestSync asks Google to re-sync devices for a user.
func (s *Service) pushGoogleRequestSync(ctx context.Context, agentUserID, saToken string) {
	const url = "https://homegraph.googleapis.com/v1/devices:requestSync"
	body, _ := json.Marshal(map[string]string{"agentUserId": agentUserID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+saToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("google requestSync user=%s: %v", agentUserID, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("google requestSync user=%s: status %d", agentUserID, resp.StatusCode)
	}
}
