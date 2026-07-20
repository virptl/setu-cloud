package app_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/setucore/setu-cloud/internal/api/middleware"
	"github.com/setucore/setu-cloud/internal/app"
)

func TestRooms_Unauthorized(t *testing.T) {
	handler := app.Rooms(nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/rooms", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d for unauthorized rooms request, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestScenes_Unauthorized(t *testing.T) {
	handler := app.Scenes(nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/scenes", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d for unauthorized scenes request, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestAutomations_Unauthorized(t *testing.T) {
	handler := app.Automations(nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/automations", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d for unauthorized automations request, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestGroups_Unauthorized(t *testing.T) {
	handler := app.Groups(nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/groups", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d for unauthorized groups request, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestCreateRoom_Unauthorized(t *testing.T) {
	handler := app.CreateRoom(nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/rooms", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d for unauthorized create room, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestCreateRoom_AuthenticatedInvalidJSON(t *testing.T) {
	handler := app.CreateRoom(nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/rooms", bytes.NewBufferString(`{invalid json`))
	ctx := middleware.WithUserContext(req.Context(), "setu", "user-123", "owner")
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status %d for invalid json in create room, got %d", http.StatusBadRequest, rec.Code)
	}
}
