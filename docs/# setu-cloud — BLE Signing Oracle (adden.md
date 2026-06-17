# setu-cloud — BLE Signing Oracle (addendum to the consumer module)

**Audience:** the SSH coding session on the VPS (repo `setu-cloud`, branch `master`).
**Why:** first-time BLE pairing requires the **cloud** to sign the device's challenge
nonce — the EC private key never leaves the server (the device verifies the signature
against the `cloud_pk` written to it at factory time). The app relays
`device_id + nonce + role` to this endpoint and gets back a raw `r‖s` signature.

Authoritative wire contract: `setu-firmware-develop/docs/BLE_PROVISIONING_CONTRACT.md` §5.

---

## 1. What the device expects (exactly)

- Message to sign: **`device_id ‖ nonce ‖ role`** — ASCII concatenation, **no separators**.
  - `device_id` and `nonce` come from the device's `auth_challenge` (nonce = 24 hex chars).
  - `role` e.g. `"owner"`.
- Signature = **ECDSA P-256 over `SHA-256(message)`**, output as **raw `r‖s`** (each
  32-byte big-endian) → **128 lowercase hex chars**. **Not** ASN.1/DER — the firmware
  uses `uECC_verify`, which wants raw `r‖s`.
- Verified on-device against the provisioned `cloud_pk`. **The private key you sign with
  here MUST be the counterpart of the `cloud_pk` burned into the device at factory
  provisioning.** (Same keypair the factory tool used / that `CLOUD_PUBKEY_HEX` exposes.)

---

## 2. Key material / config

setu-cloud already has `CloudPubkeyHex` (the public half, returned in ZTP). Add the
**private** half:

`internal/config/config.go`:
```go
CloudPrivKeyHex string // P-256 private scalar D, 64 hex chars (32 bytes)
```
```go
CloudPrivKeyHex: env("CLOUD_PRIVKEY_HEX", ""),
```

`.env`:
```env
CLOUD_PRIVKEY_HEX=<64 hex chars = the D scalar of the cloud EC keypair>
```

> If you only have the keypair as PEM, extract D: `openssl ec -in cloud.key -text -noout`
> → the `priv:` bytes (strip colons). Sanity-check that its public point equals
> `CLOUD_PUBKEY_HEX` (the `X‖Y` you already provision to devices).

---

## 3. Endpoint

```
POST /v1/ble/sign        (auth: app user JWT — middleware.AuthUser)
```

**Request**
```json
{ "device_id": "1cc3abb49b94", "nonce": "a4f2b1c0d3e5f60718293a4b", "role": "owner" }
```

**Response 200**
```json
{ "sig": "<128 hex chars>" }
```

**Errors:** `400 bad_request` (missing fields / bad hex), `500 internal` (key not
configured / sign failure).

---

## 4. Handler — `internal/app/ble.go`

```go
package app

import (
    "crypto/ecdsa"
    "crypto/elliptic"
    "crypto/rand"
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "math/big"
    "net/http"

    "github.com/setucore/setu-cloud/internal/config"
)

func SignBLENonce(cfg *config.Config) http.HandlerFunc {
    // parse the configured private key once
    var priv *ecdsa.PrivateKey
    if d, err := hex.DecodeString(cfg.CloudPrivKeyHex); err == nil && len(d) == 32 {
        k := new(ecdsa.PrivateKey)
        k.Curve = elliptic.P256()
        k.D = new(big.Int).SetBytes(d)
        k.PublicKey.X, k.PublicKey.Y = elliptic.P256().ScalarBaseMult(d)
        priv = k
    }

    return func(w http.ResponseWriter, r *http.Request) {
        if priv == nil {
            writeErr(w, 500, "internal", "cloud signing key not configured"); return
        }
        var b struct{ DeviceID, Nonce, Role string }
        if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.DeviceID == "" || b.Nonce == "" {
            writeErr(w, 400, "bad_request", "device_id and nonce required"); return
        }
        if b.Role == "" { b.Role = "owner" }

        // message = device_id ‖ nonce ‖ role  (ASCII, no separators)
        msg := b.DeviceID + b.Nonce + b.Role
        h := sha256.Sum256([]byte(msg))

        rr, ss, err := ecdsa.Sign(rand.Reader, priv, h[:])
        if err != nil { writeErr(w, 500, "internal", "sign failed"); return }

        // raw r‖s, each left-padded to 32 bytes
        sig := make([]byte, 64)
        rr.FillBytes(sig[0:32])
        ss.FillBytes(sig[32:64])
        writeJSON(w, 200, map[string]string{"sig": hex.EncodeToString(sig)})
    }
}
```

> `big.Int.FillBytes` gives the fixed 32-byte big-endian encoding (handles leading
> zeros correctly). `ecdsa.Sign` is non-deterministic — fine; the device only verifies.

Route (in the authenticated `/v1` group from the consumer-module spec):
```go
r.Post("/ble/sign", app.SignBLENonce(cfg))
```

---

## 5. Claim for a real (already-provisioned) device

The consumer-module `POST /v1/devices/claim` currently creates a *mock* device row. For a
**real** device the `did` already exists in `devices` (written by ZTP / factory). Adjust
claim so that, when the `did` already exists, it **uses the existing `pid`** instead of the
type-derived one, and just creates the `app_devices` ownership row:

```go
// inside ClaimDevice, after parsing body:
var existingPID string
_ = db.QueryRow(r.Context(),
    `SELECT pid FROM devices WHERE tid=$1 AND did=$2`, cfg.ConsumerTID, did).Scan(&existingPID)
pid := prof.PID
if existingPID != "" { pid = existingPID }   // real device: trust the provisioned pid
// ...then the existing INSERT devices ON CONFLICT DO NOTHING + INSERT app_devices, using `pid`.
```

The app passes the `did` it learned from `auth_challenge`:
`POST /v1/devices/claim {"did":"<did>","name":"...","room":"...","type":"...","icon":"..."}`.

---

## 6. Verify the oracle independently (before the app)

```bash
TOKEN=...   # a user JWT from /v1/auth/otp/verify
curl -s $BASE/v1/ble/sign -H "authorization: Bearer $TOKEN" \
  -H 'content-type: application/json' \
  -d '{"device_id":"1cc3abb49b94","nonce":"a4f2b1c0d3e5f60718293a4b","role":"owner"}'
# -> {"sig":"<128 hex>"}
```

Cross-check the signature verifies against `CLOUD_PUBKEY_HEX` for
`SHA-256("1cc3abb49b94"+"a4f2…"+"owner")` using any P-256 verifier (raw r‖s). If it
verifies cloud-side with that pubkey, the device (same pubkey) will accept it.

---

## 7. Security notes

- Rate-limit `/v1/ble/sign` per user (e.g. 10/min). Proof-of-possession is implicit:
  only someone in BLE range of the device receives the `nonce`.
- Optional hardening: refuse to sign for a `did` that is already claimed by **another**
  user (look up `app_devices`), so a signature can't be farmed for a device someone else owns.
- Keep `CLOUD_PRIVKEY_HEX` only in the server environment; never return it.

---

## 8. Acceptance

1. `POST /v1/ble/sign` returns a 128-hex signature.
2. The signature verifies against `CLOUD_PUBKEY_HEX` for the SHA-256 of
   `device_id‖nonce‖role` (raw r‖s).
3. On a real device, the app's `auth` step (relaying this sig) returns
   `{"status":"authorized", ...}` rather than `auth_failed`.
```
