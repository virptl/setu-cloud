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
	UID  string `json:"uid,omitempty"`
	Role string `json:"role"`
	jwt.RegisteredClaims
}

const uidKey contextKey = "uid"
const roleKey contextKey = "role"

// AuthUser validates a consumer (app user) JWT and injects tid + uid.
func AuthUser(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenStr := bearerToken(r)
			if tokenStr == "" {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			claims := &Claims{}
			_, err := jwt.ParseWithClaims(tokenStr, claims,
				func(t *jwt.Token) (interface{}, error) {
					if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
						return nil, jwt.ErrSignatureInvalid
					}
					return []byte(secret), nil
				})
			if err != nil || claims.UID == "" {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			ctx := context.WithValue(r.Context(), tidKey, claims.TID)
			ctx = context.WithValue(ctx, uidKey, claims.UID)
			ctx = context.WithValue(ctx, roleKey, claims.Role)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// WithUserContext injects tid, uid, and role into context for testing and internal calls.
func WithUserContext(ctx context.Context, tid, uid, role string) context.Context {
	ctx = context.WithValue(ctx, tidKey, tid)
	ctx = context.WithValue(ctx, uidKey, uid)
	return context.WithValue(ctx, roleKey, role)
}

// UIDFromContext extracts the user ID injected by AuthUser middleware.
func UIDFromContext(ctx context.Context) string {
	uid, _ := ctx.Value(uidKey).(string)
	return uid
}

// RoleFromContext extracts the role injected by AuthUser middleware.
func RoleFromContext(ctx context.Context) string {
	role, _ := ctx.Value(roleKey).(string)
	return role
}

// bearerToken extracts a JWT from the Authorization header or ?token= query param.
func bearerToken(r *http.Request) string {
	if raw := r.Header.Get("Authorization"); strings.HasPrefix(raw, "Bearer ") {
		return strings.TrimPrefix(raw, "Bearer ")
	}
	return r.URL.Query().Get("token")
}

// Auth validates a Bearer JWT and injects tid into the request context.
func Auth(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenStr := bearerToken(r)
			if tokenStr == "" {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}

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
