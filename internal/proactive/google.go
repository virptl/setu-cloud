package proactive

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/setucore/setu-cloud/internal/app"
	"github.com/setucore/setu-cloud/internal/google"
)

const googleReportStateURL = "https://homegraph.googleapis.com/v1/devices:reportStateAndNotification"

// pushGoogleReportState sends a Report State notification to Google Home Graph.
// googleSAToken is a short-lived Bearer token issued from the service account (caller's responsibility).
// agentUserID is the SetuIoT user UUID.
// Non-fatal: logs on error.
func pushGoogleReportState(ctx context.Context, did, pid, agentUserID, googleSAToken string, dps map[string]json.RawMessage) {
	if googleSAToken == "" {
		return
	}

	caps := app.CapsForPID(pid)
	state := google.DPSToState(caps, dps, true)

	body := map[string]any{
		"requestId":   uuid.New().String(),
		"agentUserId": agentUserID,
		"payload": map[string]any{
			"devices": map[string]any{
				"states": map[string]any{
					did: state,
				},
				"notificationSupportedByAgent": false,
			},
		},
		"eventId":   uuid.New().String(),
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}

	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleReportStateURL, bytes.NewReader(b))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+googleSAToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("google report state did=%s: %v", did, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		log.Printf("google report state did=%s: status %d", did, resp.StatusCode)
	}
}
