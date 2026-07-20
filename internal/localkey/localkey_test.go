package localkey_test

import (
	"crypto/rand"
	"testing"

	"github.com/setucore/setu-cloud/internal/localkey"
)

func TestNew_InvalidKEK(t *testing.T) {
	_, err := localkey.New(nil, []byte("too-short"), nil)
	if err == nil {
		t.Errorf("expected error for KEK of invalid length")
	}
}

func TestNew_ValidKEK(t *testing.T) {
	kek := make([]byte, 32)
	rand.Read(kek)

	svc, err := localkey.New(nil, kek, nil)
	if err != nil {
		t.Fatalf("unexpected error creating localkey service: %v", err)
	}
	if svc == nil {
		t.Fatalf("expected non-nil service")
	}
}

func TestConstants(t *testing.T) {
	if localkey.KeyLen != 16 {
		t.Errorf("expected KeyLen to be 16, got %d", localkey.KeyLen)
	}
	if localkey.TCPPort != 6053 {
		t.Errorf("expected TCPPort to be 6053, got %d", localkey.TCPPort)
	}
	if localkey.BeaconPort != 6054 {
		t.Errorf("expected BeaconPort to be 6054, got %d", localkey.BeaconPort)
	}
	if localkey.HKDFSalt != "setu-lan-v1" {
		t.Errorf("expected HKDFSalt to be setu-lan-v1, got %s", localkey.HKDFSalt)
	}
}
