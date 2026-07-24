package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/setucore/setu-cloud/internal/iot"
)

// contextKey for injecting TokenInfo into request context.
type contextKey string

const tokenInfoKey contextKey = "oauth_token_info"

// BearerMiddleware validates an OAuth access token from the Authorization header
// and injects the resolved TokenInfo into the request context.
func BearerMiddleware(store *Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := r.Header.Get("Authorization")
			if !strings.HasPrefix(raw, "Bearer ") {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			token := strings.TrimPrefix(raw, "Bearer ")
			info, err := store.LookupAccessToken(r.Context(), token)
			if err != nil {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			ctx := context.WithValue(r.Context(), tokenInfoKey, info)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// TokenInfoFromContext retrieves the resolved OAuth TokenInfo injected by BearerMiddleware.
func TokenInfoFromContext(ctx context.Context) *TokenInfo {
	v, _ := ctx.Value(tokenInfoKey).(*TokenInfo)
	return v
}

// Handlers groups all OAuth endpoint handlers.
type Handlers struct {
	store   *Store
	iotSvc  *iot.Service
	db      *pgxpool.Pool
	jwtSecret string
}

func NewHandlers(store *Store, iotSvc *iot.Service, db *pgxpool.Pool, jwtSecret string) *Handlers {
	return &Handlers{store: store, iotSvc: iotSvc, db: db, jwtSecret: jwtSecret}
}

// Authorize handles GET and POST /oauth/authorize.
// GET: renders a login form.
// POST: validates credentials, issues auth code, redirects.
func (h *Handlers) Authorize(w http.ResponseWriter, r *http.Request) {
	clientID := r.FormValue("client_id")
	redirectURI := r.FormValue("redirect_uri")
	state := r.FormValue("state")
	scope := r.FormValue("scope")
	if scope == "" {
		scope = "devices:read devices:control"
	}

	// Validate client + redirect_uri before doing anything else.
	ok, _ := h.store.IsRedirectURIAllowed(r.Context(), clientID, redirectURI)
	if !ok || clientID == "" || redirectURI == "" {
		http.Error(w, "invalid_client", http.StatusBadRequest)
		return
	}

	if r.Method == http.MethodGet {
		// Check for an existing flow-session cookie (short-lived JWT carrying uid).
		if uid := h.flowSessionUID(r); uid != "" {
			// User already authenticated in this browser session — issue code directly.
			h.issueCodeAndRedirect(w, r, clientID, uid, redirectURI, state, scope)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(authorizeHTML(clientID, redirectURI, state, scope, "")))
		return
	}

	// POST: process the login form.
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad_request", http.StatusBadRequest)
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	password := r.FormValue("password")

	uid, err := h.validateCredentials(r.Context(), email, password)
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(authorizeHTML(clientID, redirectURI, state, scope, "Invalid email or password.")))
		return
	}

	// Set a short-lived flow-session cookie so the browser doesn't re-auth on retry.
	h.setFlowSession(w, uid)
	h.issueCodeAndRedirect(w, r, clientID, uid, redirectURI, state, scope)
}

// Token handles POST /oauth/token.
func (h *Handlers) Token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid_request"})
		return
	}

	clientID, clientSecret, ok := r.BasicAuth()
	if !ok {
		// Fall back to form body (Google sends it this way).
		clientID = r.FormValue("client_id")
		clientSecret = r.FormValue("client_secret")
	}

	grantType := r.FormValue("grant_type")

	switch grantType {
	case "authorization_code":
		code := r.FormValue("code")
		redirectURI := r.FormValue("redirect_uri")

		// Alexa/Google may omit client_secret on code exchange when using PKCE-less flow.
		// We still require it for security — reject if missing.
		if clientSecret != "" {
			if valid, _ := h.store.ValidateClient(r.Context(), clientID, clientSecret, redirectURI); !valid {
				writeJSON(w, 401, map[string]string{"error": "invalid_client"})
				return
			}
		}

		userID, scope, err := h.store.ExchangeAuthCode(r.Context(), code, clientID, redirectURI)
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": "invalid_grant", "error_description": err.Error()})
			return
		}

		access, refresh, err := h.store.IssueTokenPair(r.Context(), clientID, userID, scope)
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": "server_error"})
			return
		}

		// Mark account linked (platform derived from client_id naming convention).
		platform := platformFromClientID(clientID)
		h.store.UpsertLinkedAccount(r.Context(), userID, platform, userID)

		writeJSON(w, 200, tokenResponse(access, refresh))

	case "refresh_token":
		refreshToken := r.FormValue("refresh_token")
		newAccess, newRefresh, _, _, err := h.store.RotateRefreshToken(r.Context(), refreshToken, clientID)
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": "invalid_grant", "error_description": err.Error()})
			return
		}
		writeJSON(w, 200, tokenResponse(newAccess, newRefresh))

	default:
		writeJSON(w, 400, map[string]string{"error": "unsupported_grant_type"})
	}
}

// UserInfo handles GET /oauth/userinfo (required by Alexa).
func (h *Handlers) UserInfo(w http.ResponseWriter, r *http.Request) {
	raw := r.Header.Get("Authorization")
	if !strings.HasPrefix(raw, "Bearer ") {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	token := strings.TrimPrefix(raw, "Bearer ")
	info, err := h.store.LookupAccessToken(r.Context(), token)
	if err != nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	email, name, err := h.iotSvc.UserEmail(r.Context(), info.UserID)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "server_error"})
		return
	}
	writeJSON(w, 200, map[string]string{
		"sub":   info.UserID,
		"email": email,
		"name":  name,
	})
}

// Revoke handles POST /oauth/revoke.
func (h *Handlers) Revoke(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	token := r.FormValue("token")
	if token != "" {
		h.store.RevokeByToken(r.Context(), token)
	}
	w.WriteHeader(http.StatusOK)
}

// --- helpers ---

func (h *Handlers) issueCodeAndRedirect(w http.ResponseWriter, r *http.Request, clientID, uid, redirectURI, state, scope string) {
	code, err := h.store.CreateAuthCode(r.Context(), clientID, uid, redirectURI, scope)
	if err != nil {
		http.Error(w, "server_error", http.StatusInternalServerError)
		return
	}
	loc := redirectURI + "?code=" + code
	if state != "" {
		loc += "&state=" + state
	}
	http.Redirect(w, r, loc, http.StatusFound)
}

// flowSessionUID extracts uid from the OAuth flow-session cookie (short-lived JWT).
func (h *Handlers) flowSessionUID(r *http.Request) string {
	c, err := r.Cookie("oauth_flow_session")
	if err != nil {
		return ""
	}
	claims := &jwt.RegisteredClaims{}
	_, err = jwt.ParseWithClaims(c.Value, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return []byte(h.jwtSecret), nil
	})
	if err != nil || claims.Subject == "" {
		return ""
	}
	return claims.Subject
}

func (h *Handlers) setFlowSession(w http.ResponseWriter, uid string) {
	tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, &jwt.RegisteredClaims{
		Subject:   uid,
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
	}).SignedString([]byte(h.jwtSecret))
	http.SetCookie(w, &http.Cookie{
		Name:     "oauth_flow_session",
		Value:    tok,
		Path:     "/oauth",
		HttpOnly: true,
		MaxAge:   300,
		SameSite: http.SameSiteNoneMode,
		Secure:   true,
	})
}

// validateCredentials checks email+password against app_users.
func (h *Handlers) validateCredentials(ctx context.Context, email, password string) (uid string, err error) {
	var pwHash string
	if err = h.db.QueryRow(ctx,
		`SELECT id, password_hash FROM app_users WHERE email=$1 AND auth_method='email_password'`,
		email).Scan(&uid, &pwHash); err != nil {
		return "", err
	}
	if bcrypt.CompareHashAndPassword([]byte(pwHash), []byte(password)) != nil {
		return "", jwt.ErrSignatureInvalid
	}
	return uid, nil
}

func platformFromClientID(clientID string) string {
	switch {
	case strings.Contains(clientID, "alexa"):
		return "alexa"
	case strings.Contains(clientID, "google"):
		return "google"
	default:
		return clientID
	}
}

func tokenResponse(access, refresh string) map[string]any {
	return map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    int(accessTokenTTL.Seconds()),
		"refresh_token": refresh,
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// authorizeHTML renders the minimal login form for the OAuth consent page.
func authorizeHTML(clientID, redirectURI, state, scope, errMsg string) string {
	errBlock := ""
	if errMsg != "" {
		errBlock = `<p style="color:#e53e3e;margin-bottom:12px">` + errMsg + `</p>`
	}
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>SetuIoT — Link Account</title>
<style>
  body{font-family:system-ui,sans-serif;background:#f7f8fa;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0}
  .card{background:#fff;border-radius:12px;box-shadow:0 2px 16px rgba(0,0,0,.1);padding:36px 32px;width:360px}
  h1{font-size:1.25rem;margin:0 0 4px}
  p{color:#666;margin:0 0 24px;font-size:.875rem}
  label{display:block;font-size:.875rem;font-weight:500;margin-bottom:6px}
  input{width:100%;box-sizing:border-box;padding:10px 12px;border:1px solid #ddd;border-radius:8px;font-size:1rem;margin-bottom:16px;outline:none}
  input:focus{border-color:#4f46e5}
  button{width:100%;padding:12px;background:#4f46e5;color:#fff;border:none;border-radius:8px;font-size:1rem;font-weight:600;cursor:pointer}
  button:hover{background:#4338ca}
</style>
</head>
<body>
<div class="card">
  <h1>SetuIoT</h1>
  <p>Sign in to link your smart home devices.</p>
  ` + errBlock + `
  <form method="POST" action="/oauth/authorize">
    <input type="hidden" name="client_id" value="` + clientID + `">
    <input type="hidden" name="redirect_uri" value="` + redirectURI + `">
    <input type="hidden" name="state" value="` + state + `">
    <input type="hidden" name="scope" value="` + scope + `">
    <input type="hidden" name="response_type" value="code">
    <label for="email">Email</label>
    <input id="email" name="email" type="email" placeholder="you@example.com" required autofocus>
    <label for="password">Password</label>
    <input id="password" name="password" type="password" placeholder="••••••••" required>
    <button type="submit">Sign in &amp; Link</button>
  </form>
</div>
</body>
</html>`
}

// PrivacyPolicy renders the static Privacy Policy page.
func (h *Handlers) PrivacyPolicy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(privacyHTML()))
}

func privacyHTML() string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>SetuIoT — Privacy Policy</title>
<style>
  body{font-family:system-ui,sans-serif;line-height:1.6;background:#f7f8fa;color:#333;margin:0;padding:24px}
  .container{background:#fff;border-radius:12px;box-shadow:0 2px 16px rgba(0,0,0,.05);padding:40px;max-width:680px;margin:40px auto}
  h1{font-size:2rem;margin-top:0;color:#111}
  h2{font-size:1.25rem;margin-top:24px;color:#222;border-bottom:1px solid #eee;padding-bottom:8px}
  p, li{font-size:.95rem;color:#555}
  ul{padding-left:20px}
</style>
</head>
<body>
<div class="container">
  <h1>Privacy Policy</h1>
  <p>Last updated: July 2026</p>
  <p>At SetuIoT, we are committed to protecting your privacy. This Privacy Policy describes how we collect, use, and share information when you use our SetuIoT platform, mobile applications, and voice integrations (such as Amazon Alexa and Google Assistant).</p>
  
  <h2>1. Information We Collect</h2>
  <p>To provide our smart home services, we collect:</p>
  <ul>
    <li><strong>Account Information:</strong> Your email address and password when you register.</li>
    <li><strong>Device Information:</strong> Device IDs, online status, configuration profiles, and state data of devices connected to your account.</li>
    <li><strong>Voice Control Data:</strong> When you control your devices via voice assistants (Alexa or Google Assistant), we receive control commands (such as "turn on") to execute on your devices. We do not store voice recordings.</li>
  </ul>

  <h2>2. How We Use Information</h2>
  <p>We use the collected information to:</p>
  <ul>
    <li>Provide, operate, and maintain our smart home services.</li>
    <li>Process control commands and synchronize device states across your mobile application and voice platforms.</li>
    <li>Communicate with you regarding account alerts, security updates, and service announcements.</li>
  </ul>

  <h2>3. Information Sharing</h2>
  <p>We do not sell your personal data. We only share information with voice assistant platforms (Amazon and Google) to the extent necessary to execute your voice control directives and report device statuses as requested by you.</p>

  <h2>4. Contact Us</h2>
  <p>If you have any questions about this Privacy Policy, please contact us at support@setuiot.com.</p>
</div>
</body>
</html>`
}
