// ota-sign — sign an OTA image hash with a tenant's cloud key (ES256, raw r||s).
//
// The WB3L firmware verifies OTA images with the tenant cloud pubkey stored in
// its NVS; this produces the matching signature over SHA-256 of firmware.uf2.
//
// usage:
//   sha256sum firmware.uf2                       # on the build machine
//   ota-sign <tid> <sha256-hex>                  # -> sig=<128 hex r||s>
// env: DATABASE_URL, KEY_ENCRYPTION_KEY (source setu-cloud/.env)
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/setucore/setu-cloud/internal/keystore"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: ota-sign <tid> <sha256-hex-of-firmware.uf2>")
		os.Exit(2)
	}
	tid := os.Args[1]
	hash, err := hex.DecodeString(os.Args[2])
	if err != nil || len(hash) != 32 {
		fmt.Fprintln(os.Stderr, "error: 2nd arg must be a 64-char SHA-256 hex")
		os.Exit(2)
	}

	kek, err := hex.DecodeString(os.Getenv("KEY_ENCRYPTION_KEY"))
	must(err, "decode KEY_ENCRYPTION_KEY")
	pool, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	must(err, "db connect")
	defer pool.Close()

	ks, err := keystore.New(pool, kek)
	must(err, "keystore init")
	tk, err := ks.ActiveKey(context.Background(), tid)
	must(err, "load tenant key")

	r, s, err := ecdsa.Sign(rand.Reader, tk.PrivKey, hash)
	must(err, "sign")

	sig := make([]byte, 64) // raw r||s, each left-padded to 32B big-endian
	r.FillBytes(sig[0:32])
	s.FillBytes(sig[32:64])

	fmt.Printf("pub=%s\n", tk.PubKeyHex)
	fmt.Printf("sig=%s\n", hex.EncodeToString(sig))
}

func must(err error, what string) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", what+":", err)
		os.Exit(1)
	}
}
