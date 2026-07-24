// Package oauth implements the OAuth 2.0 Authorization Code flow used for
// Alexa and Google Home account linking.
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const (
	accessTokenTTL  = time.Hour
	refreshTokenTTL = 30 * 24 * time.Hour
	authCodeTTL     = 10 * time.Minute
)

// Store handles all OAuth2 DB operations.
type Store struct {
	db *pgxpool.Pool
}

func NewStore(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

// TokenInfo is the result of validating an access token.
type TokenInfo struct {
	UserID   string
	ClientID string
	Scope    string
}

// --- Token generation helpers ---

func secureToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// --- OAuth clients ---

// ValidateClient checks that clientID + secret match and that redirectURI is allowed.
func (s *Store) ValidateClient(ctx context.Context, clientID, secret, redirectURI string) (bool, error) {
	var hash string
	var uris []string
	err := s.db.QueryRow(ctx,
		`SELECT client_secret, redirect_uris FROM oauth_clients WHERE client_id=$1`, clientID).
		Scan(&hash, &uris)
	if err != nil {
		return false, nil // not found
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(secret)) != nil {
		return false, nil
	}
	for _, u := range uris {
		if u == redirectURI {
			return true, nil
		}
	}
	return false, nil
}

// IsRedirectURIAllowed checks only the redirect URI without requiring a secret (used by authorize endpoint).
func (s *Store) IsRedirectURIAllowed(ctx context.Context, clientID, redirectURI string) (bool, error) {
	var uris []string
	err := s.db.QueryRow(ctx,
		`SELECT redirect_uris FROM oauth_clients WHERE client_id=$1`, clientID).Scan(&uris)
	if err != nil {
		return false, nil
	}
	for _, u := range uris {
		if u == redirectURI {
			return true, nil
		}
	}
	return false, nil
}

// --- Authorization codes ---

// CreateAuthCode generates a new authorization code and stores it.
func (s *Store) CreateAuthCode(ctx context.Context, clientID, userID, redirectURI, scope string) (string, error) {
	code, err := secureToken()
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(ctx,
		`INSERT INTO oauth_auth_codes (code, client_id, user_id, redirect_uri, scope, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		code, clientID, userID, redirectURI, scope, time.Now().Add(authCodeTTL))
	if err != nil {
		return "", fmt.Errorf("store auth code: %w", err)
	}
	return code, nil
}

// ExchangeAuthCode validates the code and returns the user + client it was issued to.
// The code is consumed (used_at set) atomically. Returns error if invalid/expired/used.
func (s *Store) ExchangeAuthCode(ctx context.Context, code, clientID, redirectURI string) (userID, scope string, err error) {
	var storedClientID, storedRedirectURI string
	var usedAt *time.Time
	var expiresAt time.Time

	err = s.db.QueryRow(ctx, `
		SELECT client_id, user_id, redirect_uri, scope, used_at, expires_at
		FROM oauth_auth_codes WHERE code=$1`,
		code).Scan(&storedClientID, &userID, &storedRedirectURI, &scope, &usedAt, &expiresAt)
	if err != nil {
		return "", "", fmt.Errorf("invalid code")
	}
	if storedClientID != clientID {
		return "", "", fmt.Errorf("client mismatch")
	}
	if storedRedirectURI != redirectURI {
		return "", "", fmt.Errorf("redirect_uri mismatch")
	}
	if usedAt != nil {
		return "", "", fmt.Errorf("code already used")
	}
	if time.Now().After(expiresAt) {
		return "", "", fmt.Errorf("code expired")
	}

	s.db.Exec(ctx, `UPDATE oauth_auth_codes SET used_at=NOW() WHERE code=$1`, code)
	return userID, scope, nil
}

// --- Access / refresh tokens ---

// IssueTokenPair generates a new access+refresh token pair, invalidating any
// existing pair for the same user+client.
func (s *Store) IssueTokenPair(ctx context.Context, clientID, userID, scope string) (accessToken, refreshToken string, err error) {
	accessToken, err = secureToken()
	if err != nil {
		return
	}
	refreshToken, err = secureToken()
	if err != nil {
		return
	}

	// Revoke previous tokens for this user+client so only one pair is active.
	s.db.Exec(ctx,
		`UPDATE oauth_tokens SET revoked_at=NOW()
		 WHERE client_id=$1 AND user_id=$2 AND revoked_at IS NULL`, clientID, userID)

	_, err = s.db.Exec(ctx, `
		INSERT INTO oauth_tokens
		    (id, client_id, user_id, access_token_hash, refresh_token_hash, scope, expires_at, refresh_expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		uuid.New(), clientID, userID,
		hashToken(accessToken), hashToken(refreshToken),
		scope,
		time.Now().Add(accessTokenTTL),
		time.Now().Add(refreshTokenTTL))
	if err != nil {
		err = fmt.Errorf("store token pair: %w", err)
	}
	return
}

// RotateRefreshToken exchanges an existing refresh token for a new pair.
func (s *Store) RotateRefreshToken(ctx context.Context, refreshToken, clientID string) (newAccess, newRefresh, userID, scope string, err error) {
	hash := hashToken(refreshToken)

	var id string
	var storedClientID string
	var revokedAt *time.Time
	var refreshExpiresAt time.Time

	err = s.db.QueryRow(ctx, `
		SELECT id, client_id, user_id, scope, revoked_at, refresh_expires_at
		FROM oauth_tokens WHERE refresh_token_hash=$1`, hash).
		Scan(&id, &storedClientID, &userID, &scope, &revokedAt, &refreshExpiresAt)
	if err != nil {
		return "", "", "", "", fmt.Errorf("invalid refresh token")
	}
	if storedClientID != clientID {
		return "", "", "", "", fmt.Errorf("client mismatch")
	}
	if revokedAt != nil {
		return "", "", "", "", fmt.Errorf("token revoked")
	}
	if time.Now().After(refreshExpiresAt) {
		return "", "", "", "", fmt.Errorf("refresh token expired")
	}

	// Revoke old record before issuing new pair.
	s.db.Exec(ctx, `UPDATE oauth_tokens SET revoked_at=NOW() WHERE id=$1`, id)

	newAccess, newRefresh, err = s.IssueTokenPair(ctx, clientID, userID, scope)
	return
}

// LookupAccessToken validates a Bearer token and returns the associated user.
func (s *Store) LookupAccessToken(ctx context.Context, token string) (*TokenInfo, error) {
	hash := hashToken(token)
	var info TokenInfo
	var revokedAt *time.Time
	var expiresAt time.Time

	err := s.db.QueryRow(ctx, `
		SELECT client_id, user_id, scope, revoked_at, expires_at
		FROM oauth_tokens WHERE access_token_hash=$1`, hash).
		Scan(&info.ClientID, &info.UserID, &info.Scope, &revokedAt, &expiresAt)
	if err != nil {
		return nil, fmt.Errorf("token not found")
	}
	if revokedAt != nil {
		return nil, fmt.Errorf("token revoked")
	}
	if time.Now().After(expiresAt) {
		return nil, fmt.Errorf("token expired")
	}
	return &info, nil
}

// RevokeByToken marks the token (access or refresh) as revoked.
func (s *Store) RevokeByToken(ctx context.Context, token string) {
	hash := hashToken(token)
	s.db.Exec(ctx, `UPDATE oauth_tokens SET revoked_at=NOW() WHERE access_token_hash=$1 OR refresh_token_hash=$1`, hash)
}

// --- Linked accounts ---

// UpsertLinkedAccount records that a user has linked a voice platform.
func (s *Store) UpsertLinkedAccount(ctx context.Context, userID, platform, platformUserID string) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO linked_accounts (user_id, platform, platform_user_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, platform) DO UPDATE
		  SET platform_user_id = EXCLUDED.platform_user_id,
		      unlinked_at = NULL,
		      linked_at = NOW()`,
		userID, platform, platformUserID)
	return err
}

// UpdateAlexaBearerToken stores the latest Alexa bearer token for proactive push.
func (s *Store) UpdateAlexaBearerToken(ctx context.Context, userID, token string) {
	s.db.Exec(ctx, `
		UPDATE linked_accounts SET alexa_bearer_token=$1
		WHERE user_id=$2 AND platform='alexa' AND unlinked_at IS NULL`, token, userID)
}

// LinkedPlatforms returns the platforms a user has linked (for proactive push dispatch).
type LinkedPlatform struct {
	Platform        string
	AlexaBearerToken string
	AgentUserID     string // google: user UUID
}

func (s *Store) LinkedPlatforms(ctx context.Context, userID string) ([]LinkedPlatform, error) {
	rows, err := s.db.Query(ctx, `
		SELECT platform, COALESCE(alexa_bearer_token,''), COALESCE(platform_user_id, $1)
		FROM linked_accounts
		WHERE user_id=$1 AND unlinked_at IS NULL`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []LinkedPlatform
	for rows.Next() {
		var lp LinkedPlatform
		rows.Scan(&lp.Platform, &lp.AlexaBearerToken, &lp.AgentUserID)
		out = append(out, lp)
	}
	return out, nil
}

// OwnerOfDevice returns the app_users.id that owns a given did.
func (s *Store) OwnerOfDevice(ctx context.Context, did string) (string, error) {
	var uid string
	err := s.db.QueryRow(ctx,
		`SELECT owner_id FROM app_devices WHERE did=$1`, did).Scan(&uid)
	return uid, err
}
