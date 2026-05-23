package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

type contextKey string

const tidKey contextKey = "tid"

// Claims is the JWT payload.
type Claims struct {
	TID  string `json:"tid"`
	Role string `json:"role"`
	jwt.RegisteredClaims
}

// Auth validates a Bearer JWT and injects tid into the request context.
func Auth(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := r.Header.Get("Authorization")
			if !strings.HasPrefix(raw, "Bearer ") {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			tokenStr := strings.TrimPrefix(raw, "Bearer ")

			claims := &Claims{}
			_, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
				if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, jwt.ErrSignatureInvalid
				}
				return []byte(secret), nil
			})
			if err != nil || claims.TID == "" {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), tidKey, claims.TID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// TIDFromContext extracts the tenant ID injected by the Auth middleware.
func TIDFromContext(ctx context.Context) string {
	tid, _ := ctx.Value(tidKey).(string)
	return tid
}
