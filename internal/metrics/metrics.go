package metrics

import (
	"fmt"
	"net/http"
	"runtime"
	"sync/atomic"
	"time"
)

var (
	startTime      = time.Now()
	totalRequests  uint64
	activeRequests int64
)

// Middleware records request statistics.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddUint64(&totalRequests, 1)
		atomic.AddInt64(&activeRequests, 1)
		defer atomic.AddInt64(&activeRequests, -1)

		next.ServeHTTP(w, r)
	})
}

// Handler outputs metrics in Prometheus exposition format.
func Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uptime := time.Since(startTime).Seconds()
		var memStats runtime.MemStats
		runtime.ReadMemStats(&memStats)

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		fmt.Fprintf(w, "# HELP setu_cloud_uptime_seconds Total process uptime in seconds\n")
		fmt.Fprintf(w, "# TYPE setu_cloud_uptime_seconds counter\n")
		fmt.Fprintf(w, "setu_cloud_uptime_seconds %.2f\n\n", uptime)

		fmt.Fprintf(w, "# HELP setu_cloud_requests_total Total number of HTTP requests processed\n")
		fmt.Fprintf(w, "# TYPE setu_cloud_requests_total counter\n")
		fmt.Fprintf(w, "setu_cloud_requests_total %d\n\n", atomic.LoadUint64(&totalRequests))

		fmt.Fprintf(w, "# HELP setu_cloud_requests_active Current active HTTP requests\n")
		fmt.Fprintf(w, "# TYPE setu_cloud_requests_active gauge\n")
		fmt.Fprintf(w, "setu_cloud_requests_active %d\n\n", atomic.LoadInt64(&activeRequests))

		fmt.Fprintf(w, "# HELP setu_cloud_goroutines Current number of goroutines\n")
		fmt.Fprintf(w, "# TYPE setu_cloud_goroutines gauge\n")
		fmt.Fprintf(w, "setu_cloud_goroutines %d\n\n", runtime.NumGoroutine())

		fmt.Fprintf(w, "# HELP setu_cloud_mem_alloc_bytes Alloc memory in bytes\n")
		fmt.Fprintf(w, "# TYPE setu_cloud_mem_alloc_bytes gauge\n")
		fmt.Fprintf(w, "setu_cloud_mem_alloc_bytes %d\n", memStats.Alloc)
	}
}
