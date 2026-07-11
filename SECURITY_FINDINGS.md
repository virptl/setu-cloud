# setu-cloud Security Findings

**Review date:** 2026-06-18  
**Scope:** All API endpoints (REST, WebSocket, Admin panel, ZTP, BLE)  
**Method:** Static code analysis — authenticated and unauthenticated attack paths

---

## Findings Summary

| # | File | Severity | Category | Confidence |
|---|------|----------|----------|------------|
| [S-1](#s-1--otp-dev-mode-code-leaked-in-http-response) | `internal/app/auth.go:75-78` | HIGH | auth_bypass via OTP dev-mode default | 9/10 |
| [S-2](#s-2--unauthenticated-ztp-factory-provisioning) | `internal/ztp/handler.go:37-41` | HIGH | auth_bypass unauthenticated ZTP provision | 9/10 |
| [S-3](#s-3--ble-signing-oracle-guest-can-escalate-to-admin-role) | `internal/app/ble.go:36-43` | HIGH | privilege_escalation BLE sign oracle | 9/10 |
| [S-4](#s-4--plaintext-mqtt-credentials-in-admin-fleet-api) | `admin/main.go:62,371-388` | HIGH | data_exposure plaintext MQTT creds | 9/10 |

All four findings are **independently exploitable**, require no special tooling, and two (S-1, S-2) are reachable with zero prior credentials on a default deployment.

> **Note:** Switching from HTTP to HTTPS does NOT fix any of these vulnerabilities. They are all server-side logic/authorization flaws, not transport-layer issues. See [Why HTTPS Is Not Enough](#why-https-is-not-enough).

---

## S-1 — OTP Dev-Mode Code Leaked in HTTP Response

**File:** `internal/app/auth.go:75-78`  
**Config:** `internal/config/config.go:58`  
**Severity:** HIGH | **Confidence:** 9/10  
**Category:** `auth_bypass` / `data_exposure`

### Description

`OTP_DEV_MODE` defaults to `"true"` when the environment variable is not set:

```go
// internal/config/config.go:58
OTPDevMode: env("OTP_DEV_MODE", "true") == "true",
```

When dev mode is active, the plaintext OTP code is embedded directly in the HTTP response body:

```go
// internal/app/auth.go:75-78
if cfg.OTPDevMode {
    log.Printf("[OTP] %s -> %s", email, code)
    resp["dev_code"] = code
}
```

Any deployment that does not explicitly set `OTP_DEV_MODE=false` ships in this state — no operator action required to be vulnerable.

### Exploit Scenario

1. Attacker calls `POST /v1/auth/otp/request` with victim's email — no auth required.
2. Response: `{"sent":true,"dev_code":"482931"}` — OTP code returned in body.
3. Attacker immediately calls `POST /v1/auth/otp/verify` with the returned code.
4. Server issues a valid JWT session for the victim — **full account takeover, email ownership bypassed**.

### Fix

Change the default to `false`:

```go
OTPDevMode: env("OTP_DEV_MODE", "false") == "true",
```

Or use `must()` to panic on startup if the variable is not explicitly set, forcing a conscious deployment decision:

```go
OTPDevMode: mustBool("OTP_DEV_MODE"),
```

---

## S-2 — Unauthenticated ZTP Factory Provisioning

**File:** `internal/ztp/handler.go:37-41`  
**Config:** `internal/config/config.go:53`  
**Severity:** HIGH | **Confidence:** 9/10  
**Category:** `auth_bypass`

### Description

The factory provisioning endpoint `POST /factory/provision` is registered outside all authenticated middleware groups. Its token guard is wrapped in a conditional that is never entered when `FACTORY_PROV_TOKEN` is empty:

```go
// internal/ztp/handler.go:37-41
if cfg.FactoryProvToken != "" {
    if r.Header.Get("X-Factory-Token") != cfg.FactoryProvToken {
        http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
        return
    }
}
```

`FACTORY_PROV_TOKEN` defaults to `""` (config line 53), so the auth block is silently skipped on any deployment that omits the env var.

### Exploit Scenario

1. Attacker sends `POST /factory/provision {"mac":"aa:bb:cc:dd:ee:ff"}` — no token header needed.
2. Server responds with: MQTT username, MQTT password, broker URI, tenant ID, device ID, cloud public key.
3. Attacker connects to the MQTT broker as that device — injects forged telemetry, intercepts commands.
4. Device is marked as provisioned in the database — **legitimate device locked out of ZTP**.

### Fix

Use `must()` (consistent with other required secrets in config) so startup panics if the token is not configured:

```go
FactoryProvToken: must("FACTORY_PROV_TOKEN"),
```

Alternatively, add a startup check that panics with a clear message:

```go
if cfg.FactoryProvToken == "" {
    log.Fatal("FACTORY_PROV_TOKEN must be set — factory endpoint is unauthenticated without it")
}
```

---

## S-3 — BLE Signing Oracle: Guest Can Escalate to Admin Role on Any Unclaimed Device

**File:** `internal/app/ble.go:36-43`  
**Severity:** HIGH | **Confidence:** 9/10  
**Category:** `auth_bypass` / `privilege_escalation`

### Description

`POST /v1/ble/sign` accepts zero-verification guest JWTs (issued by `POST /v1/auth/guest`, which requires no email, phone, or identity). The ownership check has a fail-open flaw: for any device with no `app_devices` row (unclaimed), `ownerID` stays `""` and the guard evaluates false:

```go
// internal/app/ble.go:36-43
var ownerID string
db.QueryRow(r.Context(),
    `SELECT owner_id FROM app_devices WHERE did=$1`, b.DeviceID).Scan(&ownerID)
if ownerID != "" && ownerID != uid {          // ← false when device is unclaimed
    writeErr(w, 403, "forbidden", "device already claimed by another user")
    return
}
```

Additionally, the caller's `role` field is written directly into the signed payload with no server-side allowlist — only an empty `role` is substituted with `"owner"`. Any non-empty string (including `"admin"`) passes straight through.

The server signs `device_id ‖ nonce ‖ role` with the tenant's P-256 private key. Device firmware validates this with `uECC_verify` against the embedded cloud public key and grants access accordingly.

### Exploit Scenario

1. Attacker calls `POST /v1/auth/guest` — zero verification — receives a valid JWT.
2. Attacker calls `POST /v1/ble/sign` with `{"device_id":"<unclaimed-did>","nonce":"<any>","role":"admin"}`.
3. Server produces a valid tenant-key-signed ECDSA credential for role `"admin"`.
4. Attacker presents this over BLE to the physical device — **firmware grants admin-level BLE access**.

This affects all newly shipped hardware (provisioned in the `devices` table but not yet claimed in `app_devices` — the normal state for devices in the field).

### Fix

Three changes required:

```go
// 1. Reject guest tokens at this endpoint
if claims.Role == "guest" {
    writeErr(w, 403, "forbidden", "guests cannot sign BLE credentials")
    return
}

// 2. Fail closed: treat unclaimed device as unauthorized
if ownerID == "" || ownerID != uid {
    writeErr(w, 403, "forbidden", "not the device owner")
    return
}

// 3. Enforce server-side role allowlist
validRoles := map[string]bool{"owner": true, "user": true}
if b.Role != "" && !validRoles[b.Role] {
    writeErr(w, 400, "bad_request", "invalid role")
    return
}
```

---

## S-4 — Plaintext MQTT Credentials Returned in Admin Fleet API

**File:** `admin/main.go:62`, `admin/main.go:371-388`  
**Severity:** HIGH | **Confidence:** 9/10  
**Category:** `data_exposure`

### Description

The `GET /api/devices` endpoint serializes every device record including `mq_pass` in full plaintext:

```go
// admin/main.go:62
type device struct {
    MQPass string `json:"mq_pass"`
    // ...
}

// admin/main.go:371-388  — SELECT includes mq_pass
// SELECT mac, tid, did, pid, mq_user, mq_pass, hw_config, ...
```

MQTT passwords for the entire device fleet are returned on every fleet list request. There is no functional reason for the admin UI to receive these credentials — they are provisioned once at device-creation time and are only needed by the physical device itself.

### Exploit Scenario

1. Attacker compromises any admin session (credential stuffing, phishing, insider, session hijacking).
2. Calls `GET /api/devices` — receives MQTT credentials for **every device in the fleet**.
3. Connects to the MQTT broker as any or all devices — forges telemetry, intercepts commands, pushes arbitrary payloads fleet-wide.

### Fix

Remove `mq_pass` from the list endpoint SQL query and JSON struct:

```go
// Remove mq_pass from SELECT and from the device struct json tag
// Change json tag to omit or rename:
MQPass string `json:"-"`
```

If credential rotation is ever needed, implement a dedicated endpoint:

```
POST /api/devices/{id}/rotate-mqtt-credentials
```

---

## Why HTTPS Is Not Enough

Switching from HTTP to HTTPS only encrypts data **in transit**. It does not fix any of these vulnerabilities:

| Finding | HTTPS fixes it? | Reason |
|---------|:--------------:|---------|
| S-1: OTP in response body | No | The plaintext code is in the JSON payload — the attacker making the request receives it regardless of TLS |
| S-2: Unauthenticated ZTP | No | Missing authentication — attacker calls the endpoint over HTTPS just as easily |
| S-3: BLE sign oracle | No | Broken authorization logic — guest registers over HTTPS and still gets the signed admin credential |
| S-4: MQTT creds in API | Partially | HTTPS stops passive eavesdropping, but any attacker with a valid admin session still receives all fleet MQTT passwords |

---

## Remediation Priority

| Priority | Finding | Effort |
|----------|---------|--------|
| P0 — Fix immediately | S-1: OTP default dev-mode on | 1 line change |
| P0 — Fix immediately | S-2: ZTP unauthenticated | 1 line change (use `must()`) |
| P1 — Fix before next release | S-3: BLE signing oracle | ~10 lines, 3 guard conditions |
| P1 — Fix before next release | S-4: MQTT creds in fleet API | Remove field from SQL + struct |
