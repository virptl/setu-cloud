package app

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	mrand "math/rand"
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

func issueToken(secret, tid, uid, role string, ttl time.Duration) (string, error) {
	claims := &middleware.Claims{
		TID: tid, UID: uid, Role: role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

func mintSession(cfg *config.Config, uid, email, name, role string, isGuest bool) session {
	access, _ := issueToken(cfg.JWTSecret, cfg.ConsumerTID, uid, role, 15*time.Minute)
	refresh, _ := issueToken(cfg.JWTSecret, cfg.ConsumerTID, uid, role, 30*24*time.Hour)
	return session{
		User:         userDTO{ID: uid, Email: email, IsGuest: isGuest, DisplayName: name},
		AccessToken:  access,
		RefreshToken: refresh,
	}
}

func secureToken() (string, error) {
	b := make([]byte, 32)
	if _, err := cryptorand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
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

		code := fmt.Sprintf("%06d", mrand.Intn(1000000))
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
// Returns a short-lived verificationToken — does NOT create a user or session.
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

		// Fetch most recent unconsumed OTP — include expired ones so we can
		// distinguish "no code" (400) from "expired" (410).
		var id, hash string
		var attempts int
		var valid bool
		err := db.QueryRow(r.Context(), `
			SELECT id, code_hash, attempts, expires_at > NOW()
			FROM otp_codes
			WHERE email=$1 AND consumed_at IS NULL
			ORDER BY created_at DESC LIMIT 1`, email).Scan(&id, &hash, &attempts, &valid)
		if err != nil {
			writeErr(w, 400, "invalid_code", "no pending code")
			return
		}
		if !valid {
			writeErr(w, 410, "code_expired", "request a new code")
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

		// Mint single-use verification token (10 min TTL).
		token, err := secureToken()
		if err != nil {
			writeErr(w, 500, "server_error", "could not generate token")
			return
		}
		if _, err = db.Exec(r.Context(),
			`INSERT INTO verification_tokens (id, email, token, expires_at) VALUES ($1, $2, $3, $4)`,
			uuid.New(), email, token, time.Now().Add(10*time.Minute)); err != nil {
			writeErr(w, 500, "server_error", "could not store token")
			return
		}
		writeJSON(w, 200, map[string]string{"verificationToken": token})
	}
}

// Register handles POST /v1/auth/register.
// Validates verificationToken from /otp/verify, creates the user, returns a session.
func Register(db *pgxpool.Pool, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Email             string `json:"email"`
			VerificationToken string `json:"verificationToken"`
			Name              string `json:"name"`
			Password          string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "bad_request", "invalid json")
			return
		}
		email := strings.ToLower(strings.TrimSpace(body.Email))
		name := strings.TrimSpace(body.Name)
		if email == "" || body.VerificationToken == "" || name == "" {
			writeErr(w, 400, "bad_request", "email, verificationToken, and name are required")
			return
		}
		if len(body.Password) < 8 {
			writeErr(w, 400, "bad_request", "password must be at least 8 characters")
			return
		}

		// Validate verification token.
		var vtID, vtEmail string
		var vtConsumedAt *time.Time
		var vtExpired bool
		err := db.QueryRow(r.Context(), `
			SELECT id, email, consumed_at, expires_at < NOW()
			FROM verification_tokens WHERE token=$1`,
			body.VerificationToken).Scan(&vtID, &vtEmail, &vtConsumedAt, &vtExpired)
		if err != nil {
			writeErr(w, 400, "invalid_token", "verification token not found")
			return
		}
		if vtConsumedAt != nil {
			writeErr(w, 400, "invalid_token", "verification token already used")
			return
		}
		if vtExpired {
			writeErr(w, 410, "token_expired", "verification token expired, restart OTP flow")
			return
		}
		if vtEmail != email {
			writeErr(w, 400, "invalid_token", "token does not match email")
			return
		}

		// Consume the token before any writes so concurrent requests fail cleanly.
		db.Exec(r.Context(), `UPDATE verification_tokens SET consumed_at=NOW() WHERE id=$1`, vtID)

		// Reject if email already registered.
		var existingID string
		if err = db.QueryRow(r.Context(),
			`SELECT id FROM app_users WHERE email=$1`, email).Scan(&existingID); err == nil {
			writeErr(w, 409, "email_taken", "email already registered")
			return
		}

		pwHash, err := bcrypt.GenerateFromPassword([]byte(body.Password), 12)
		if err != nil {
			writeErr(w, 500, "server_error", "could not hash password")
			return
		}

		uid := uuid.New().String()
		if _, err = db.Exec(r.Context(), `
			INSERT INTO app_users (id, email, display_name, password_hash, auth_method, email_verified_at)
			VALUES ($1, $2, $3, $4, 'email_password', NOW())`,
			uid, email, name, string(pwHash)); err != nil {
			writeErr(w, 500, "server_error", "could not create user")
			return
		}

		writeJSON(w, 200, mintSession(cfg, uid, email, name, "user", false))
	}
}

// Login handles POST /v1/auth/login.
func Login(db *pgxpool.Pool, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "bad_request", "invalid json")
			return
		}
		email := strings.ToLower(strings.TrimSpace(body.Email))
		if email == "" || body.Password == "" {
			writeErr(w, 401, "invalid_credentials", "invalid email or password")
			return
		}

		var uid, name, pwHash string
		var failedAttempts int
		var lockedUntil *time.Time
		err := db.QueryRow(r.Context(), `
			SELECT id, display_name, password_hash, failed_login_attempts, locked_until
			FROM app_users WHERE email=$1 AND auth_method='email_password'`,
			email).Scan(&uid, &name, &pwHash, &failedAttempts, &lockedUntil)
		if err != nil {
			writeErr(w, 401, "invalid_credentials", "invalid email or password")
			return
		}

		if lockedUntil != nil && time.Now().Before(*lockedUntil) {
			writeErr(w, 429, "too_many_attempts", "account temporarily locked, try again later")
			return
		}

		if bcrypt.CompareHashAndPassword([]byte(pwHash), []byte(body.Password)) != nil {
			newAttempts := failedAttempts + 1
			if newAttempts >= 5 {
				db.Exec(r.Context(), `
					UPDATE app_users SET failed_login_attempts=$1, locked_until=NOW()+INTERVAL '5 minutes'
					WHERE id=$2`, newAttempts, uid)
			} else {
				db.Exec(r.Context(),
					`UPDATE app_users SET failed_login_attempts=$1 WHERE id=$2`, newAttempts, uid)
			}
			writeErr(w, 401, "invalid_credentials", "invalid email or password")
			return
		}

		// Success — reset lockout state.
		db.Exec(r.Context(),
			`UPDATE app_users SET failed_login_attempts=0, locked_until=NULL WHERE id=$1`, uid)

		writeJSON(w, 200, mintSession(cfg, uid, email, name, "user", false))
	}
}

// Guest handles POST /v1/auth/guest.
func Guest(db *pgxpool.Pool, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := uuid.New().String()
		db.Exec(r.Context(),
			`INSERT INTO app_users (id, display_name, is_guest) VALUES ($1, 'Guest', true)`, uid)
		tok, _ := issueToken(cfg.JWTSecret, cfg.ConsumerTID, uid, "guest", 30*24*time.Hour)
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
