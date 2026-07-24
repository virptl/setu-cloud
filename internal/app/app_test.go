package app_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/setucore/setu-cloud/internal/app"
)

func TestSignBLENonce_MissingParams(t *testing.T) {
	handler := app.SignBLENonce(nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/ble/sign", bytes.NewBufferString(`{"device_id":""}`))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status %d for missing device_id/nonce, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestSignBLENonce_InvalidRole(t *testing.T) {
	handler := app.SignBLENonce(nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/ble/sign", bytes.NewBufferString(`{"device_id":"12345678901234567890123","nonce":"123456","role":"admin"}`))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status %d for unauthorized role, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestIsAssistantSupported(t *testing.T) {
	// Devices configured with assistant config (e.g. light-rgbcw, light1, th1, sp1)
	if !app.IsAssistantSupported(nil, nil, "light-rgbcw", "alexa") {
		t.Errorf("Expected light-rgbcw to support Alexa")
	}
	if !app.IsAssistantSupported(nil, nil, "light-rgbcw", "google") {
		t.Errorf("Expected light-rgbcw to support Google")
	}
	if !app.IsAssistantSupported(nil, nil, "sp1", "alexa") {
		t.Errorf("Expected sp1 to support Alexa")
	}

	// Devices WITHOUT assistant config (e.g. gen1 or unknown PID)
	if app.IsAssistantSupported(nil, nil, "gen1", "alexa") {
		t.Errorf("Expected gen1 NOT to support Alexa")
	}
	if app.IsAssistantSupported(nil, nil, "gen1", "google") {
		t.Errorf("Expected gen1 NOT to support Google")
	}
	if app.IsAssistantSupported(nil, nil, "unknown_pid", "alexa") {
		t.Errorf("Expected unknown_pid NOT to support Alexa")
	}
}
