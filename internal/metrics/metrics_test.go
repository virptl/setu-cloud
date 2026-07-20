package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/setucore/setu-cloud/internal/metrics"
)

func TestMetricsMiddlewareAndHandler(t *testing.T) {
	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := metrics.Middleware(dummyHandler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	metricsHandler := metrics.Handler()
	mReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	mRec := httptest.NewRecorder()
	metricsHandler.ServeHTTP(mRec, mReq)

	if mRec.Code != http.StatusOK {
		t.Errorf("expected 200 OK for /metrics, got %d", mRec.Code)
	}

	body := mRec.Body.String()
	if !strings.Contains(body, "setu_cloud_requests_total") {
		t.Errorf("expected metrics output to contain setu_cloud_requests_total, got: %s", body)
	}
	if !strings.Contains(body, "setu_cloud_uptime_seconds") {
		t.Errorf("expected metrics output to contain setu_cloud_uptime_seconds, got: %s", body)
	}
}
