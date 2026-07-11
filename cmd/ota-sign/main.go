// ota-sign — sign an OTA image for the WB3L firmware (ES256, raw r||s).
//
// The firmware verifies the signature over  SHA-256(uf2) || epoch(be32)  with
// the tenant cloud pubkey in its NVS, and rejects epoch < its own SETU_FW_EPOCH
// (anti-rollback). Pass the epoch to produce that binding; omit it for a legacy
// hash-only signature (bootstrapping a pre-anti-rollback device).
//
// usage:
//   sha256sum firmware.uf2                        # on the build machine
//   ota-sign <tid> <sha256-hex> <epoch>           # -> sig=<128 hex r||s>
//   ota-sign <tid> <sha256-hex>                   # legacy (no epoch binding)
// env: DATABASE_URL, KEY_ENCRYPTION_KEY (source setu-cloud/.env)
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/setucore/setu-cloud/internal/keystore"
)

func main() {
	if len(os.Args) < 3 || len(os.Args) > 4 {
		fmt.Fprintln(os.Stderr, "usage: ota-sign <tid> <sha256-hex-of-firmware.uf2> [epoch]")
		os.Exit(2)
	}
	tid := os.Args[1]
	hash, err := hex.DecodeString(os.Args[2])
	if err != nil || len(hash) != 32 {
		fmt.Fprintln(os.Stderr, "error: 2nd arg must be a 64-char SHA-256 hex")
		os.Exit(2)
	}

	// digest to sign. With an epoch, bind it (anti-rollback):
	//   digest = SHA-256( sha256(uf2) || epoch_be32 )   [matches es256_verify(msg)]
	// else sign the raw image hash (legacy / bootstrap).
	digest := hash
	epochStr := "(none)"
	if len(os.Args) == 4 {
		epoch, err := strconv.ParseUint(os.Args[3], 10, 32)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: epoch must be a uint32")
			os.Exit(2)
		}
		msg := make([]byte, 36)
		copy(msg, hash)
		binary.BigEndian.PutUint32(msg[32:], uint32(epoch))
		d := sha256.Sum256(msg)
		digest = d[:]
		epochStr = os.Args[3]
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

	r, s, err := ecdsa.Sign(rand.Reader, tk.PrivKey, digest)
	must(err, "sign")

	sig := make([]byte, 64) // raw r||s, each left-padded to 32B big-endian
	r.FillBytes(sig[0:32])
	s.FillBytes(sig[32:64])

	fmt.Printf("pub=%s\n", tk.PubKeyHex)
	fmt.Printf("epoch=%s\n", epochStr)
	fmt.Printf("sig=%s\n", hex.EncodeToString(sig))
}

func must(err error, what string) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", what+":", err)
		os.Exit(1)
	}
}
