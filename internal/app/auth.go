package app

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/setucore/setu-cloud/internal/api/middleware"
	"github.com/setucore/setu-cloud/internal/config"
	emailsvc "github.com/setucore/setu-cloud/internal/email"
)

type userDTO struct {
	ID          string `json:"id"`
	Email       string `json:"email,omitempty"`
	IsGuest     bool   `json:"isGuest"`
	DisplayName string `json:"displayName,omitempty"`
}

type session struct {
	User         userDTO `json:"user"`
	AccessToken  string  `json:"accessToken"`
	RefreshToken string  `json:"refreshToken"`
}

func issueToken(secret, tid, uid, role string) (string, error) {
	claims := &middleware.Claims{
		TID: tid, UID: uid, Role: role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(30 * 24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

// RequestOTP handles POST /v1/auth/otp/request.
func RequestOTP(db *pgxpool.Pool, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Email string `json:"email"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Email == "" {
			writeErr(w, 400, "bad_request", "email required")
			return
		}
		email := strings.ToLower(strings.TrimSpace(body.Email))

		// Rate limit: reject if an unconsumed code was created in the last 30s.
		var recent int
		db.QueryRow(r.Context(),
			`SELECT count(*) FROM otp_codes WHERE email=$1 AND consumed_at IS NULL AND created_at > NOW()-interval '30 seconds'`,
			email).Scan(&recent)
		if recent > 0 {
			writeJSON(w, 200, map[string]bool{"sent": true})
			return
		}

		code := fmt.Sprintf("%06d", rand.Intn(1000000))
		hash, _ := bcrypt.GenerateFromPassword([]byte(code), bcrypt.DefaultCost)
		db.Exec(r.Context(),
			`INSERT INTO otp_codes (id, email, code_hash, expires_at) VALUES ($1, $2, $3, $4)`,
			uuid.New(), email, string(hash),
			time.Now().Add(time.Duration(cfg.OTPTTLMinutes)*time.Minute))

		resp := map[string]any{"sent": true}
		if cfg.OTPDevMode {
			log.Printf("[OTP] %s -> %s", email, code)
			resp["dev_code"] = code
		} else {
			if err := emailsvc.SendOTP(
				cfg.SMTPHost, cfg.SMTPPort,
				cfg.SMTPUser, cfg.SMTPPassword,
				cfg.SMTPFrom, email, code,
			); err != nil {
				log.Printf("[OTP] email send failed for %s: %v", email, err)
			}
		}
		writeJSON(w, 200, resp)
	}
}

// VerifyOTP handles POST /v1/auth/otp/verify.
func VerifyOTP(db *pgxpool.Pool, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Email string `json:"email"`
			Code  string `json:"code"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "bad_request", "invalid json")
			return
		}
		email := strings.ToLower(strings.TrimSpace(body.Email))

		var id, hash string
		var attempts int
		err := db.QueryRow(r.Context(), `
			SELECT id, code_hash, attempts FROM otp_codes
			WHERE email=$1 AND consumed_at IS NULL AND expires_at > NOW()
			ORDER BY created_at DESC LIMIT 1`, email).Scan(&id, &hash, &attempts)
		if err != nil {
			writeErr(w, 400, "invalid_code", "code expired or not found")
			return
		}
		if attempts >= 5 {
			writeErr(w, 429, "too_many_attempts", "request a new code")
			return
		}
		db.Exec(r.Context(), `UPDATE otp_codes SET attempts=attempts+1 WHERE id=$1`, id)
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(strings.TrimSpace(body.Code))) != nil {
			writeErr(w, 400, "invalid_code", "wrong code")
			return
		}
		db.Exec(r.Context(), `UPDATE otp_codes SET consumed_at=NOW() WHERE id=$1`, id)

		// Upsert user.
		var uid, name string
		err = db.QueryRow(r.Context(),
			`SELECT id, COALESCE(display_name,'') FROM app_users WHERE email=$1`, email).Scan(&uid, &name)
		if err != nil {
			uid = uuid.New().String()
			name = strings.Split(email, "@")[0]
			db.Exec(r.Context(),
				`INSERT INTO app_users (id, email, email_verified_at, display_name) VALUES ($1, $2, NOW(), $3)`,
				uid, email, name)
		} else {
			db.Exec(r.Context(), `UPDATE app_users SET email_verified_at=NOW() WHERE id=$1`, uid)
		}

		tok, _ := issueToken(cfg.JWTSecret, cfg.ConsumerTID, uid, "user")
		writeJSON(w, 200, session{
			User:         userDTO{ID: uid, Email: email, DisplayName: name},
			AccessToken:  tok,
			RefreshToken: tok,
		})
	}
}

// Guest handles POST /v1/auth/guest.
func Guest(db *pgxpool.Pool, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := uuid.New().String()
		db.Exec(r.Context(),
			`INSERT INTO app_users (id, display_name, is_guest) VALUES ($1, 'Guest', true)`, uid)
		tok, _ := issueToken(cfg.JWTSecret, cfg.ConsumerTID, uid, "guest")
		writeJSON(w, 200, session{
			User:         userDTO{ID: uid, IsGuest: true, DisplayName: "Guest"},
			AccessToken:  tok,
			RefreshToken: tok,
		})
	}
}

// Logout handles POST /v1/auth/logout — stateless JWT, no-op for MVP.
func Logout() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}
}
