package ztp_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/setucore/setu-cloud/internal/config"
	"github.com/setucore/setu-cloud/internal/ztp"
)

func TestHandleProvision_UnauthorizedMissingHeader(t *testing.T) {
	cfg := &config.Config{
		FactoryProvToken: "secret-factory-token",
	}

	handler := ztp.HandleProvision(nil, cfg, nil)

	req := httptest.NewRequest(http.MethodPost, "/factory/provision", bytes.NewBufferString(`{"mac":"AABBCCDDEEFF"}`))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestHandleProvision_UnauthorizedEmptyTokenInConfig(t *testing.T) {
	cfg := &config.Config{
		FactoryProvToken: "",
	}

	handler := ztp.HandleProvision(nil, cfg, nil)

	req := httptest.NewRequest(http.MethodPost, "/factory/provision", bytes.NewBufferString(`{"mac":"AABBCCDDEEFF"}`))
	// Even if request supplies matching empty header, it should be unauthorized
	req.Header.Set("X-Factory-Token", "")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d when config token is empty, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestHandleProvision_InvalidMAC(t *testing.T) {
	cfg := &config.Config{
		FactoryProvToken: "secret-factory-token",
	}

	handler := ztp.HandleProvision(nil, cfg, nil)

	req := httptest.NewRequest(http.MethodPost, "/factory/provision", bytes.NewBufferString(`{"mac":"invalid-mac-format"}`))
	req.Header.Set("X-Factory-Token", "secret-factory-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status %d for invalid MAC, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestHandleProvision_InvalidJSON(t *testing.T) {
	cfg := &config.Config{
		FactoryProvToken: "secret-factory-token",
	}

	handler := ztp.HandleProvision(nil, cfg, nil)

	req := httptest.NewRequest(http.MethodPost, "/factory/provision", bytes.NewBufferString(`{invalid json`))
	req.Header.Set("X-Factory-Token", "secret-factory-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status %d for invalid JSON, got %d", http.StatusBadRequest, rec.Code)
	}
}
