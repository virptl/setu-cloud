package middleware

import (
	"net/http"
	"strings"
)

// ServiceToken guards service-to-service / admin endpoints (released-products
// ingest from dev_portal, inventory seeding from the admin portal). The caller
// must present the shared token as `Authorization: Bearer <token>` or in the
// `X-Service-Token` header. An empty configured token rejects all requests so
// the endpoints fail closed when the deployment forgets to set it.
func ServiceToken(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token == "" || !validServiceToken(r, token) {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func validServiceToken(r *http.Request, token string) bool {
	if h := r.Header.Get("X-Service-Token"); h != "" {
		return h == token
	}
	if raw := r.Header.Get("Authorization"); strings.HasPrefix(raw, "Bearer ") {
		return strings.TrimPrefix(raw, "Bearer ") == token
	}
	return false
}
