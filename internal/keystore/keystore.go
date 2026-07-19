// Package keystore manages per-tenant P-256 signing keypairs.
// Private keys are stored AES-256-GCM encrypted in Postgres; the key
// encryption key (KEK) lives only in the server's environment.
// Active keys are cached in memory (5-min TTL) to avoid per-request DB hits.
package keystore

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const cacheTTL = 5 * time.Minute

// TenantKey holds the active keypair for one tenant.
type TenantKey struct {
	KeyID     string
	TID       string
	PubKeyHex string // uncompressed X‖Y, 128 hex chars
	PrivKey   *ecdsa.PrivateKey
	CreatedAt time.Time
}

type cachedEntry struct {
	key      *TenantKey
	cachedAt time.Time
}

// Service is safe for concurrent use.
type Service struct {
	db  *pgxpool.Pool
	kek []byte // 32-byte AES-256 KEK, never logged or stored in DB

	mu    sync.RWMutex
	cache map[string]*cachedEntry // tid → active key
}

func New(db *pgxpool.Pool, kek []byte) (*Service, error) {
	if len(kek) != 32 {
		return nil, errors.New("keystore: KEK must be exactly 32 bytes")
	}
	return &Service{
		db:    db,
		kek:   kek,
		cache: make(map[string]*cachedEntry),
	}, nil
}

// Generate creates a fresh P-256 keypair for tid, encrypts the private scalar,
// persists it as the new active key (atomically deactivating the previous one),
// and updates the in-memory cache.
func (s *Service) Generate(ctx context.Context, tid string) (*TenantKey, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate keypair: %w", err)
	}

	// Public key: uncompressed X‖Y, each left-padded to 32 bytes.
	pub := make([]byte, 64)
	priv.PublicKey.X.FillBytes(pub[:32])
	priv.PublicKey.Y.FillBytes(pub[32:])
	pubHex := hex.EncodeToString(pub)

	// Private scalar D, left-padded to 32 bytes.
	d := make([]byte, 32)
	priv.D.FillBytes(d)

	enc, err := encryptGCM(s.kek, d)
	if err != nil {
		return nil, fmt.Errorf("encrypt private key: %w", err)
	}

	keyID := uuid.New().String()
	now := time.Now().UTC()

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Atomically deactivate any current active key and insert the new one.
	if _, err := tx.Exec(ctx,
		`UPDATE tenant_keys SET active=false, rotated_at=NOW() WHERE tid=$1 AND active=true`,
		tid,
	); err != nil {
		return nil, fmt.Errorf("deactivate old key: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO tenant_keys (key_id, tid, pub_key_hex, priv_key_enc, active, created_at)
		 VALUES ($1, $2, $3, $4, true, $5)`,
		keyID, tid, pubHex, enc, now,
	); err != nil {
		return nil, fmt.Errorf("insert new key: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	tk := &TenantKey{
		KeyID: keyID, TID: tid,
		PubKeyHex: pubHex, PrivKey: priv, CreatedAt: now,
	}
	s.setCache(tid, tk)
	return tk, nil
}

// ActiveKey returns the active keypair for tid.
// On a cache miss or TTL expiry it fetches from Postgres.
// If no key exists for the tenant it auto-generates one (lazy init).
func (s *Service) ActiveKey(ctx context.Context, tid string) (*TenantKey, error) {
	s.mu.RLock()
	entry, ok := s.cache[tid]
	s.mu.RUnlock()
	if ok && time.Since(entry.cachedAt) < cacheTTL {
		return entry.key, nil
	}

	tk, err := s.loadFromDB(ctx, tid)
	if errors.Is(err, pgx.ErrNoRows) {
		// No key yet for this tenant — generate one automatically.
		return s.Generate(ctx, tid)
	}
	return tk, err
}

// ActivePubKey returns only the public key hex for tid. Used by ZTP.
func (s *Service) ActivePubKey(ctx context.Context, tid string) (string, error) {
	tk, err := s.ActiveKey(ctx, tid)
	if err != nil {
		return "", err
	}
	return tk.PubKeyHex, nil
}

// Invalidate evicts the cached key for tid so the next call reloads from DB.
// Call after an external key rotation.
func (s *Service) Invalidate(tid string) {
	s.mu.Lock()
	delete(s.cache, tid)
	s.mu.Unlock()
}

// ListKeyInfo returns public metadata for all tenant keys (no private material).
func (s *Service) ListKeyInfo(ctx context.Context) ([]KeyInfo, error) {
	rows, err := s.db.Query(ctx, `
		SELECT key_id, tid, pub_key_hex, active, created_at, rotated_at
		FROM tenant_keys ORDER BY tid, created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []KeyInfo
	for rows.Next() {
		var k KeyInfo
		if err := rows.Scan(&k.KeyID, &k.TID, &k.PubKeyHex,
			&k.Active, &k.CreatedAt, &k.RotatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// KeyInfo is the public-safe view of a tenant_keys row.
type KeyInfo struct {
	KeyID     string     `json:"key_id"`
	TID       string     `json:"tid"`
	PubKeyHex string     `json:"pub_key_hex"`
	Active    bool       `json:"active"`
	CreatedAt time.Time  `json:"created_at"`
	RotatedAt *time.Time `json:"rotated_at"`
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func (s *Service) loadFromDB(ctx context.Context, tid string) (*TenantKey, error) {
	var keyID, pubHex string
	var privEnc []byte
	var createdAt time.Time

	err := s.db.QueryRow(ctx,
		`SELECT key_id, pub_key_hex, priv_key_enc, created_at
		 FROM tenant_keys WHERE tid=$1 AND active=true`,
		tid,
	).Scan(&keyID, &pubHex, &privEnc, &createdAt)
	if err != nil {
		return nil, err // pgx.ErrNoRows propagates to caller
	}

	d, err := decryptGCM(s.kek, privEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt private key: %w", err)
	}

	priv := &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{Curve: elliptic.P256()},
		D:         new(big.Int).SetBytes(d),
	}
	priv.PublicKey.X, priv.PublicKey.Y = elliptic.P256().ScalarBaseMult(d)

	tk := &TenantKey{
		KeyID: keyID, TID: tid,
		PubKeyHex: pubHex, PrivKey: priv, CreatedAt: createdAt,
	}
	s.setCache(tid, tk)
	return tk, nil
}

func (s *Service) setCache(tid string, tk *TenantKey) {
	s.mu.Lock()
	s.cache[tid] = &cachedEntry{key: tk, cachedAt: time.Now()}
	s.mu.Unlock()
}

// SealGCM / OpenGCM expose this package's AES-256-GCM helpers so sibling
// packages (e.g. localkey) can encrypt small secrets at rest with the same KEK
// and wire format, without re-implementing the crypto.
func SealGCM(key, plaintext []byte) ([]byte, error)  { return encryptGCM(key, plaintext) }
func OpenGCM(key, ciphertext []byte) ([]byte, error) { return decryptGCM(key, ciphertext) }

// encryptGCM encrypts plaintext with AES-256-GCM using the given key.
// Output format: nonce(12B) ‖ ciphertext ‖ tag(16B).
func encryptGCM(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func decryptGCM(key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(ciphertext) < ns {
		return nil, errors.New("ciphertext too short")
	}
	return gcm.Open(nil, ciphertext[:ns], ciphertext[ns:], nil)
}
