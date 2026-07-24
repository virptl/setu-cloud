package alexa

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/setucore/setu-cloud/internal/app"
	"github.com/setucore/setu-cloud/internal/iot"
	"github.com/setucore/setu-cloud/internal/oauth"
)

// --- Incoming directive structures ---

type directiveHeader struct {
	Namespace        string `json:"namespace"`
	Name             string `json:"name"`
	PayloadVersion   string `json:"payloadVersion"`
	MessageID        string `json:"messageId"`
	CorrelationToken string `json:"correlationToken"`
}

type directiveScope struct {
	Type  string `json:"type"`
	Token string `json:"token"`
}

type directiveEndpoint struct {
	Scope      directiveScope `json:"scope"`
	EndpointID string         `json:"endpointId"`
}

type directivePayload struct {
	Scope   *directiveScope  `json:"scope"`   // used in Discovery, AcceptGrant
	Grant   *directiveGrant  `json:"grant"`
	Grantee *directiveScope  `json:"grantee"`
}

type directiveGrant struct {
	Type string `json:"type"`
	Code string `json:"code"`
}

type directive struct {
	Header   directiveHeader   `json:"header"`
	Endpoint directiveEndpoint `json:"endpoint"`
	Payload  json.RawMessage   `json:"payload"`
}

type request struct {
	Directive directive `json:"directive"`
}

// --- Response builder helpers ---

func responseHeader(correlationToken, namespace, name string) header {
	return header{
		Namespace:        namespace,
		Name:             name,
		PayloadVersion:   "3",
		MessageID:        uuid.New().String(),
		CorrelationToken: correlationToken,
	}
}

func errorResponse(correlationToken, endpointID, token, errType, errMsg string) map[string]any {
	return map[string]any{
		"event": map[string]any{
			"header": responseHeader(correlationToken, "Alexa", "ErrorResponse"),
			"endpoint": map[string]any{
				"scope":      map[string]string{"type": "BearerToken", "token": token},
				"endpointId": endpointID,
			},
			"payload": map[string]string{"type": errType, "message": errMsg},
		},
	}
}

func successResponse(correlationToken, endpointID, token string, props []property) map[string]any {
	return map[string]any{
		"event": map[string]any{
			"header":   responseHeader(correlationToken, "Alexa", "Response"),
			"endpoint": map[string]any{"scope": map[string]string{"type": "BearerToken", "token": token}, "endpointId": endpointID},
			"payload":  map[string]any{},
		},
		"context": map[string]any{"properties": props},
	}
}

func stateReport(correlationToken, endpointID, token string, props []property) map[string]any {
	return map[string]any{
		"event": map[string]any{
			"header":   responseHeader(correlationToken, "Alexa", "StateReport"),
			"endpoint": map[string]any{"scope": map[string]string{"type": "BearerToken", "token": token}, "endpointId": endpointID},
			"payload":  map[string]any{},
		},
		"context": map[string]any{"properties": props},
	}
}

// --- Handler ---

// Handler handles all Alexa Smart Home directives at POST /alexa/smarthome.
type Handler struct {
	iot        *iot.Service
	oauthStore *oauth.Store
}

func NewHandler(iot *iot.Service, oauthStore *oauth.Store) *Handler {
	return &Handler{iot: iot, oauthStore: oauthStore}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad_request", http.StatusBadRequest)
		return
	}

	dir := req.Directive
	ns := dir.Header.Namespace
	name := dir.Header.Name

	// Extract OAuth token from the appropriate location in the directive.
	token := dir.Endpoint.Scope.Token
	if token == "" {
		// Discovery and AcceptGrant put it in payload.scope.
		var pl directivePayload
		json.Unmarshal(dir.Payload, &pl)
		if pl.Scope != nil {
			token = pl.Scope.Token
		}
		if token == "" && pl.Grantee != nil {
			token = pl.Grantee.Token
		}
	}

	switch ns {
	case "Alexa.Authorization":
		// AcceptGrant — Alexa notifying us the skill was enabled.
		// Respond with success; the grantee token is stored for proactive push.
		if name == "AcceptGrant" && token != "" {
			info, err := h.oauthStore.LookupAccessToken(r.Context(), token)
			if err == nil {
				h.oauthStore.UpdateAlexaBearerToken(r.Context(), info.UserID, token)
			}
		}
		writeJSON(w, map[string]any{
			"event": map[string]any{
				"header":  responseHeader("", "Alexa.Authorization", "AcceptGrant.Response"),
				"payload": map[string]any{},
			},
		})
		return

	case "Alexa.Discovery":
		h.handleDiscovery(w, r, token)
		return
	}

	// All other namespaces require a valid token and endpoint.
	info, err := h.oauthStore.LookupAccessToken(r.Context(), token)
	if err != nil {
		writeJSON(w, errorResponse(dir.Header.CorrelationToken, dir.Endpoint.EndpointID, token,
			"INVALID_AUTHORIZATION_CREDENTIAL", "token invalid or expired"))
		return
	}
	// Keep the bearer token fresh for proactive push.
	h.oauthStore.UpdateAlexaBearerToken(r.Context(), info.UserID, token)

	endpointID := dir.Endpoint.EndpointID

	switch ns {
	case "Alexa":
		if name == "ReportState" {
			h.handleReportState(w, r, info, endpointID, dir.Header.CorrelationToken, token)
		}
	default:
		h.handleControl(w, r, info, endpointID, ns, name, dir.Payload, dir.Header.CorrelationToken, token)
	}
}

func (h *Handler) handleDiscovery(w http.ResponseWriter, r *http.Request, token string) {
	info, err := h.oauthStore.LookupAccessToken(r.Context(), token)
	if err != nil {
		writeJSON(w, map[string]any{
			"event": map[string]any{
				"header":  responseHeader("", "Alexa.Discovery", "Discover.Response"),
				"payload": map[string]any{"endpoints": []any{}},
			},
		})
		return
	}

	devices, err := h.iot.ListDevicesForAssistant(r.Context(), info.UserID, "alexa")
	if err != nil {
		log.Printf("alexa discovery uid=%s: %v", info.UserID, err)
		devices = nil
	}

	var endpoints []EndpointDef
	for _, d := range devices {
		caps := app.ResolveCapabilities(r.Context(), h.iot.DB(), d.PID, 0)
		consumerType := app.ResolveConsumerType(r.Context(), h.iot.DB(), d.PID)
		endpoints = append(endpoints, EndpointDef{
			EndpointID:        d.DID,
			ManufacturerName:  "SetuIoT",
			Description:       d.Name + " by SetuIoT",
			FriendlyName:      d.Name,
			DisplayCategories: displayCategory(consumerType),
			Cookie:            map[string]any{"pid": d.PID},
			Capabilities:      CapabilitiesToAlexa(caps),
		})
	}
	if endpoints == nil {
		endpoints = []EndpointDef{}
	}

	writeJSON(w, map[string]any{
		"event": map[string]any{
			"header":  responseHeader("", "Alexa.Discovery", "Discover.Response"),
			"payload": map[string]any{"endpoints": endpoints},
		},
	})
}

func (h *Handler) handleReportState(w http.ResponseWriter, r *http.Request, info *oauth.TokenInfo, did, correlationToken, token string) {
	tid, pid, ok := h.iot.OwnsDeviceForAssistant(r.Context(), info.UserID, did, "alexa")
	if !ok {
		writeJSON(w, errorResponse(correlationToken, did, token, "NO_SUCH_ENDPOINT", "device not found"))
		return
	}
	if !h.iot.IsOnline(r.Context(), tid, did) {
		writeJSON(w, errorResponse(correlationToken, did, token, "ENDPOINT_UNREACHABLE", "device offline"))
		return
	}
	dps := h.iot.GetReportedDPS(r.Context(), tid, did)
	caps := app.ResolveCapabilities(r.Context(), h.iot.DB(), pid, 0)
	ts := time.Now().UTC().Format(time.RFC3339)
	props := DPSToProperties(caps, dps, ts)
	writeJSON(w, stateReport(correlationToken, did, token, props))
}

func (h *Handler) handleControl(w http.ResponseWriter, r *http.Request, info *oauth.TokenInfo, did, ns, name string, payload json.RawMessage, correlationToken, token string) {
	tid, pid, ok := h.iot.OwnsDeviceForAssistant(r.Context(), info.UserID, did, "alexa")
	if !ok {
		writeJSON(w, errorResponse(correlationToken, did, token, "NO_SUCH_ENDPOINT", "device not found"))
		return
	}
	if !h.iot.IsOnline(r.Context(), tid, did) {
		writeJSON(w, errorResponse(correlationToken, did, token, "ENDPOINT_UNREACHABLE", "device offline"))
		return
	}

	caps := app.ResolveCapabilities(r.Context(), h.iot.DB(), pid, 0)
	dps, err := DirectiveToDPS(ns, name, payload, caps)
	if err != nil {
		writeJSON(w, errorResponse(correlationToken, did, token, "INVALID_DIRECTIVE", err.Error()))
		return
	}

	if _, err := h.iot.SendCommand(r.Context(), info.UserID, did, dps); err != nil {
		log.Printf("alexa control did=%s: %v", did, err)
		writeJSON(w, errorResponse(correlationToken, did, token, "INTERNAL_ERROR", err.Error()))
		return
	}

	// Read back reported state for the response.
	reportedDPS := h.iot.GetReportedDPS(r.Context(), tid, did)
	// Merge our commanded values in for an optimistic response.
	for dp, val := range dps {
		reportedDPS[dp] = val
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	props := DPSToProperties(caps, reportedDPS, ts)
	writeJSON(w, successResponse(correlationToken, did, token, props))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
