import os

target_path = 'internal/app/auth.go'
if not os.path.exists(target_path):
    print("Error: must run in setu-cloud repository directory")
    exit(1)

with open(target_path, 'r') as f:
    content = f.read()

# If DeleteAccount comment exists, cut it off there. Otherwise, append to the end of the file.
idx = content.find('// DeleteAccount handles DELETE /v1/auth/delete.')
if idx != -1:
    new_content = content[:idx]
else:
    new_content = content.rstrip() + "\n\n"

new_code = """// DeleteAccount handles DELETE /v1/auth/delete.
// Permanently removes the authenticated user and all associated data after verifying OTP (if registered).
// Foreign-key ON DELETE CASCADE handles app_devices, oauth_auth_codes,
// oauth_tokens, and linked_accounts automatically.
func DeleteAccount(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := middleware.UIDFromContext(r.Context())
		if uid == "" {
			writeErr(w, 401, "unauthorized", "not authenticated")
			return
		}

		var email string
		var isGuest bool
		err := db.QueryRow(r.Context(),
			`SELECT email, is_guest FROM app_users WHERE id = $1`, uid).Scan(&email, &isGuest)
		if err != nil {
			log.Printf("DeleteAccount: failed to find user %s: %v", uid, err)
			writeErr(w, 404, "not_found", "user not found")
			return
		}

		// Registered users must verify via OTP
		if !isGuest && email != "" {
			var body struct {
				Code string `json:"code"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Code == "" {
				writeErr(w, 400, "bad_request", "verification code required for registered users")
				return
			}
			code := strings.TrimSpace(body.Code)

			// Verify the OTP code
			var otpID, hash string
			var expiresAt time.Time
			var consumedAt *time.Time

			err = db.QueryRow(r.Context(),
				`SELECT id, code_hash, expires_at, consumed_at FROM otp_codes 
				 WHERE email = $1 AND consumed_at IS NULL 
				 ORDER BY created_at DESC LIMIT 1`, email).Scan(&otpID, &hash, &expiresAt, &consumedAt)
			if err != nil {
				writeErr(w, 400, "invalid_code", "no active verification code found")
				return
			}

			if time.Now().After(expiresAt) {
				writeErr(w, 410, "code_expired", "verification code has expired")
				return
			}

			if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(code)); err != nil {
				writeErr(w, 400, "invalid_code", "incorrect verification code")
				return
			}

			// Mark code as consumed
			db.Exec(r.Context(),
				`UPDATE otp_codes SET consumed_at = NOW() WHERE id = $1`, otpID)
		}

		// Proceed with deletion:
		// 1. Revoke all refresh token families for this user.
		db.Exec(r.Context(),
			`UPDATE refresh_tokens SET revoked_at = NOW() WHERE user_id = $1 AND revoked_at IS NULL`, uid)

		// 2. Delete the user row.
		tag, err := db.Exec(r.Context(),
			`DELETE FROM app_users WHERE id = $1`, uid)
		if err != nil {
			log.Printf("DeleteAccount: failed to delete user %s: %v", uid, err)
			writeErr(w, 500, "server_error", "could not delete account")
			return
		}

		if tag.RowsAffected() == 0 {
			writeErr(w, 404, "not_found", "account not found")
			return
		}

		log.Printf("DeleteAccount: user %s deleted successfully", uid)
		w.WriteHeader(http.StatusOK)
	}
}

// RequestDeleteOTP handles POST /v1/auth/delete/otp.
// Generates a 6-digit OTP and sends it to the user's email to verify account deletion.
func RequestDeleteOTP(db *pgxpool.Pool, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := middleware.UIDFromContext(r.Context())
		if uid == "" {
			writeErr(w, 401, "unauthorized", "not authenticated")
			return
		}

		var email string
		var isGuest bool
		err := db.QueryRow(r.Context(),
			`SELECT email, is_guest FROM app_users WHERE id = $1`, uid).Scan(&email, &isGuest)
		if err != nil {
			log.Printf("RequestDeleteOTP: failed to find user %s: %v", uid, err)
			writeErr(w, 404, "not_found", "user not found")
			return
		}

		if isGuest || email == "" {
			writeJSON(w, 200, map[string]any{"sent": false, "reason": "guest_user_no_otp_needed"})
			return
		}

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
			log.Printf("[Delete OTP] %s -> %s", email, code)
		} else {
			if err := emailsvc.SendOTP(
				cfg.SMTPHost, cfg.SMTPPort,
				cfg.SMTPUser, cfg.SMTPPassword,
				cfg.SMTPFrom, email, code,
			); err != nil {
				log.Printf("[Delete OTP] email send failed for %s: %v", email, err)
			}
		}
		writeJSON(w, 200, resp)
	}
}
"""

with open(target_path, 'w') as f:
    f.write(new_content + new_code)

print("Replacement successful")
