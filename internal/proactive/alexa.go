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
	"github.com/setucore/setu-cloud/internal/alexa"
)

const alexaEventGatewayURL = "https://api.amazonalexa.com/v3/events"

// pushAlexaChangeReport sends a proactive ChangeReport to the Alexa Event Gateway.
// The bearerToken is the user's Alexa access token stored in linked_accounts.
// Non-fatal: logs and returns on any error (Alexa will call ReportState next interaction).
func pushAlexaChangeReport(ctx context.Context, did, pid, bearerToken string, dps map[string]json.RawMessage) {
	if bearerToken == "" {
		return
	}

	caps := app.CapsForPID(pid)
	ts := time.Now().UTC().Format(time.RFC3339)
	props := alexa.DPSToProperties(caps, dps, ts)
	if len(props) == 0 {
		return
	}

	body := map[string]any{
		"event": map[string]any{
			"header": map[string]any{
				"namespace":      "Alexa",
				"name":           "ChangeReport",
				"payloadVersion": "3",
				"messageId":      uuid.New().String(),
			},
			"endpoint": map[string]any{
				"scope":      map[string]string{"type": "BearerToken", "token": bearerToken},
				"endpointId": did,
			},
			"payload": map[string]any{
				"change": map[string]any{
					"cause":      map[string]string{"type": "PHYSICAL_INTERACTION"},
					"properties": props,
				},
			},
		},
		"context": map[string]any{"properties": props},
	}

	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, alexaEventGatewayURL, bytes.NewReader(b))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearerToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("alexa proactive did=%s: %v", did, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		log.Printf("alexa proactive did=%s: status %d", did, resp.StatusCode)
	}
}
