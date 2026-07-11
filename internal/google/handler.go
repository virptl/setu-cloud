package google

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/setucore/setu-cloud/internal/app"
	"github.com/setucore/setu-cloud/internal/iot"
	"github.com/setucore/setu-cloud/internal/oauth"
)

// --- Google Smart Home request types ---

type intentInput struct {
	Intent  string          `json:"intent"`
	Payload json.RawMessage `json:"payload"`
}

type smarthomeRequest struct {
	RequestID string        `json:"requestId"`
	Inputs    []intentInput `json:"inputs"`
}

// SYNC payload (empty).
// QUERY payload.
type queryPayload struct {
	Devices []struct {
		ID string `json:"id"`
	} `json:"devices"`
}

// EXECUTE payload.
type executePayload struct {
	Commands []executeCommand `json:"commands"`
}

type executeCommand struct {
	Devices   []struct{ ID string `json:"id"` } `json:"devices"`
	Execution []struct {
		Command string         `json:"command"`
		Params  map[string]any `json:"params"`
	} `json:"execution"`
}

// --- Handler ---

type Handler struct {
	iot        *iot.Service
	oauthStore *oauth.Store
}

func NewHandler(iot *iot.Service, oauthStore *oauth.Store) *Handler {
	return &Handler{iot: iot, oauthStore: oauthStore}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Resolve the OAuth bearer token from Authorization header.
	raw := r.Header.Get("Authorization")
	if !strings.HasPrefix(raw, "Bearer ") {
		writeJSON(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	token := strings.TrimPrefix(raw, "Bearer ")
	info, err := h.oauthStore.LookupAccessToken(r.Context(), token)
	if err != nil {
		writeJSON(w, 401, map[string]string{"error": "unauthorized"})
		return
	}

	var req smarthomeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad_request"})
		return
	}

	if len(req.Inputs) == 0 {
		writeJSON(w, 400, map[string]string{"error": "no_inputs"})
		return
	}

	switch req.Inputs[0].Intent {
	case "action.devices.SYNC":
		h.handleSync(w, r, req.RequestID, info)
	case "action.devices.QUERY":
		h.handleQuery(w, r, req.RequestID, info, req.Inputs[0].Payload)
	case "action.devices.EXECUTE":
		h.handleExecute(w, r, req.RequestID, info, req.Inputs[0].Payload)
	case "action.devices.DISCONNECT":
		h.oauthStore.UpsertLinkedAccount(r.Context(), info.UserID, "google", "")
		writeJSON(w, 200, map[string]any{})
	default:
		writeJSON(w, 400, map[string]string{"error": "unknown_intent"})
	}
}

func (h *Handler) handleSync(w http.ResponseWriter, r *http.Request, requestID string, info *oauth.TokenInfo) {
	devices, err := h.iot.ListDevicesForUser(r.Context(), info.UserID)
	if err != nil {
		log.Printf("google sync uid=%s: %v", info.UserID, err)
		devices = nil
	}

	var googleDevices []DeviceDef
	for _, d := range devices {
		caps := app.CapsForPID(d.PID)
		consumerType := app.DeviceTypeForPID(d.PID)
		googleDevices = append(googleDevices, BuildDeviceDef(d.DID, d.PID, d.Name, consumerType, caps))
	}
	if googleDevices == nil {
		googleDevices = []DeviceDef{}
	}

	writeJSON(w, 200, map[string]any{
		"requestId": requestID,
		"payload": map[string]any{
			"agentUserId": info.UserID,
			"devices":     googleDevices,
		},
	})
}

func (h *Handler) handleQuery(w http.ResponseWriter, r *http.Request, requestID string, info *oauth.TokenInfo, rawPayload json.RawMessage) {
	var payload queryPayload
	json.Unmarshal(rawPayload, &payload)

	deviceStates := map[string]any{}
	for _, d := range payload.Devices {
		tid, pid, ok := h.iot.OwnsDevice(r.Context(), info.UserID, d.ID)
		if !ok {
			deviceStates[d.ID] = map[string]any{"status": "ERROR", "errorCode": "deviceNotFound"}
			continue
		}
		online := h.iot.IsOnline(r.Context(), tid, d.ID)
		if !online {
			deviceStates[d.ID] = map[string]any{"status": "OFFLINE", "online": false}
			continue
		}
		dps := h.iot.GetReportedDPS(r.Context(), tid, d.ID)
		caps := app.CapsForPID(pid)
		deviceStates[d.ID] = DPSToState(caps, dps, true)
	}

	writeJSON(w, 200, map[string]any{
		"requestId": requestID,
		"payload": map[string]any{
			"devices": deviceStates,
		},
	})
}

func (h *Handler) handleExecute(w http.ResponseWriter, r *http.Request, requestID string, info *oauth.TokenInfo, rawPayload json.RawMessage) {
	var payload executePayload
	json.Unmarshal(rawPayload, &payload)

	type commandResult struct {
		IDs    []string       `json:"ids"`
		Status string         `json:"status"`
		States map[string]any `json:"states,omitempty"`
		ErrorCode string      `json:"errorCode,omitempty"`
	}

	var results []commandResult

	for _, cmd := range payload.Commands {
		// Collect results per device × execution command.
		var successIDs, offlineIDs, errorIDs []string
		var lastState map[string]any
		var lastError string

		for _, dev := range cmd.Devices {
			tid, pid, ok := h.iot.OwnsDevice(r.Context(), info.UserID, dev.ID)
			if !ok {
				errorIDs = append(errorIDs, dev.ID)
				lastError = "deviceNotFound"
				continue
			}
			if !h.iot.IsOnline(r.Context(), tid, dev.ID) {
				offlineIDs = append(offlineIDs, dev.ID)
				continue
			}
			caps := app.CapsForPID(pid)
			for _, exec := range cmd.Execution {
				dps := ExecuteCommandToDPS(exec.Command, exec.Params, caps)
				if len(dps) == 0 {
					continue
				}
				if _, err := h.iot.SendCommand(r.Context(), info.UserID, dev.ID, dps); err != nil {
					log.Printf("google execute did=%s: %v", dev.ID, err)
					errorIDs = append(errorIDs, dev.ID)
					lastError = "hardError"
					continue
				}
			}
			// Build optimistic state response.
			dps := h.iot.GetReportedDPS(r.Context(), tid, dev.ID)
			for _, exec := range cmd.Execution {
				for dp, val := range ExecuteCommandToDPS(exec.Command, exec.Params, caps) {
					dps[dp] = val
				}
			}
			lastState = DPSToState(caps, dps, true)
			successIDs = append(successIDs, dev.ID)
		}

		if len(successIDs) > 0 {
			results = append(results, commandResult{IDs: successIDs, Status: "SUCCESS", States: lastState})
		}
		if len(offlineIDs) > 0 {
			results = append(results, commandResult{IDs: offlineIDs, Status: "OFFLINE"})
		}
		if len(errorIDs) > 0 {
			results = append(results, commandResult{IDs: errorIDs, Status: "ERROR", ErrorCode: lastError})
		}
	}

	if results == nil {
		results = []commandResult{}
	}

	writeJSON(w, 200, map[string]any{
		"requestId": requestID,
		"payload": map[string]any{
			"commands": results,
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
