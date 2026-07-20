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
