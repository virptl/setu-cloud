package keystore_test

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/setucore/setu-cloud/internal/keystore"
)

func TestNew_InvalidKEK(t *testing.T) {
	invalidKEK := []byte("short-kek")
	_, err := keystore.New(nil, invalidKEK)
	if err == nil {
		t.Errorf("expected error for KEK of invalid length, got nil")
	}
}

func TestNew_ValidKEK(t *testing.T) {
	kek := make([]byte, 32)
	rand.Read(kek)

	svc, err := keystore.New(nil, kek)
	if err != nil {
		t.Fatalf("unexpected error creating keystore: %v", err)
	}
	if svc == nil {
		t.Fatalf("expected non-nil keystore service")
	}
}

func TestSealGCM_OpenGCM_Roundtrip(t *testing.T) {
	kek := make([]byte, 32)
	rand.Read(kek)

	plaintext := []byte("secret-payload-to-encrypt")

	encrypted, err := keystore.SealGCM(kek, plaintext)
	if err != nil {
		t.Fatalf("failed to SealGCM: %v", err)
	}

	if bytes.Equal(encrypted, plaintext) {
		t.Errorf("ciphertext should not match plaintext")
	}

	decrypted, err := keystore.OpenGCM(kek, encrypted)
	if err != nil {
		t.Fatalf("failed to OpenGCM: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("expected %s, got %s", string(plaintext), string(decrypted))
	}
}

func TestOpenGCM_InvalidCiphertext(t *testing.T) {
	kek := make([]byte, 32)
	rand.Read(kek)

	tooShort := []byte("short")
	_, err := keystore.OpenGCM(kek, tooShort)
	if err == nil {
		t.Errorf("expected error when decrypting ciphertext that is too short")
	}

	invalidKey := make([]byte, 32)
	rand.Read(invalidKey)

	plaintext := []byte("secret-payload")
	encrypted, _ := keystore.SealGCM(kek, plaintext)

	_, err = keystore.OpenGCM(invalidKey, encrypted)
	if err == nil {
		t.Errorf("expected error when decrypting with wrong key")
	}
}
