package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/setucore/setu-cloud/internal/api/middleware"
)

type tokenRequest struct {
	APIKey string `json:"api_key"`
}

type tokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
}

// Token exchanges a tenant API key for a signed JWT.
func Token(db *pgxpool.Pool, jwtSecret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req tokenRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.APIKey == "" {
			http.Error(w, `{"error":"bad_request"}`, http.StatusBadRequest)
			return
		}

		// Fetch all tenants matching — compare hashes in Go to avoid timing oracles.
		rows, err := db.Query(r.Context(), `SELECT tid, api_key_hash FROM tenants`)
		if err != nil {
			http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var matchedTID string
		for rows.Next() {
			var tid, hash string
			rows.Scan(&tid, &hash)
			if bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.APIKey)) == nil {
				matchedTID = tid
				break
			}
		}

		if matchedTID == "" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		exp := time.Now().Add(24 * time.Hour)
		claims := &middleware.Claims{
			TID:  matchedTID,
			Role: "user",
			RegisteredClaims: jwt.RegisteredClaims{
				ExpiresAt: jwt.NewNumericDate(exp),
				IssuedAt:  jwt.NewNumericDate(time.Now()),
			},
		}
		tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(jwtSecret))
		if err != nil {
			http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tokenResponse{Token: tok, ExpiresAt: exp.Unix()})
	}
}
