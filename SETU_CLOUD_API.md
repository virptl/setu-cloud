# Setu-Cloud — Complete API & WebSocket Reference

> **Audience:** Mobile / web app developers integrating with the Setu-Cloud IoT backend.  
> **Base URL:** `https://api.setuiot.com` (replace with your deployment URL)  
> **Protocol:** HTTPS for all REST; WSS for WebSocket

---

## Table of Contents

1. [Architecture Overview](#1-architecture-overview)
2. [Authentication Overview](#2-authentication-overview)
3. [Consumer Auth APIs — `/v1/auth`](#3-consumer-auth-apis)
4. [Device APIs — `/v1/devices`](#4-device-apis)
5. [BLE Provisioning — `/v1/ble`](#5-ble-provisioning)
6. [Product Profiles — `/v1/products`](#6-product-profiles)
7. [Meta APIs — Rooms / Scenes / Automations](#7-meta-apis)
8. [Linked Accounts](#8-linked-accounts)
9. [WebSocket — Real-Time Events](#9-websocket)
10. [Platform / Tenant APIs](#10-platform--tenant-apis)
11. [Factory ZTP API](#11-factory-ztp-api)
12. [OAuth2 — Voice Assistant Account Linking](#12-oauth2--voice-assistant-account-linking)
13. [MQTT Protocol — Device Side](#13-mqtt-protocol--device-side)
14. [End-to-End Flows](#14-end-to-end-flows)
15. [Error Reference](#15-error-reference)
16. [Data Types & Capability Kinds](#16-data-types--capability-kinds)

---

## 1. Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                        Mobile / Web App                         │
│                                                                 │
│  REST /v1/*  ◄──────────────────────────────────►  WSS /ws     │
└──────────────────────┬──────────────────────────────────────────┘
                       │ HTTPS
              ┌────────▼────────┐
              │   setu-cloud    │  Go HTTP server (chi router)
              │                 │
              │  PostgreSQL     │  source of truth (devices, shadows,
              │  Redis          │  users, commands, events)
              │  EMQX (MQTT)    │  real-time device bus
              └────────┬────────┘
                       │ MQTT  setu/{tid}/{pid}/{did}/dn  (cloud→device)
                       │       setu/{tid}/{pid}/{did}/up  (device→cloud)
                       │       setu/{tid}/{pid}/{did}/shd (device shadow)
              ┌────────▼────────┐
              │   IoT Device    │  ESP32 / firmware
              └─────────────────┘
```

**Key identifiers:**

| ID | What it is |
|----|------------|
| `tid` | Tenant ID — `"setu"` for the first-party consumer app |
| `did` | Device ID — assigned at factory (ZTP), UUID format |
| `pid` | Product ID — firmware type (`light-rgbcw`, `light1`, `th1`, `sp1`, `gen1`) |
| `uid` | App user ID — UUID assigned at registration |
| `dp`  | Datapoint key — numeric string e.g. `"1"` (power), `"2"` (brightness) |

---

## 2. Authentication Overview

### Consumer App (mobile/web)

All `/v1/*` protected routes require a **JWT access token** in the `Authorization` header:

```
Authorization: Bearer <accessToken>
```

Access tokens are **short-lived (15 minutes)**. Refresh using `/v1/auth/refresh`.

The WebSocket endpoint also accepts the token as a query parameter:

```
wss://api.setuiot.com/ws?token=<accessToken>
```

### JWT Payload (decoded)

```json
{
  "tid": "setu",
  "uid": "3fa85f64-5717-4562-b3fc-2c963f66afa6",
  "role": "user",
  "iat": 1750000000,
  "exp": 1750000900
}
```

`role` is one of `user` | `guest`.

---

## 3. Consumer Auth APIs

All endpoints under `/v1/auth` are **public** (no token required).

---

### 3.1 OTP Flow — New User Registration

New users must verify their email with a 6-digit OTP before creating an account.  
Existing users should use `/v1/auth/login` instead.

#### Step 1 — Request OTP

```
POST /v1/auth/otp/request
Content-Type: application/json
```

**Request**
```json
{
  "email": "user@example.com"
}
```

**Response 200 — OTP sent**
```json
{
  "sent": true
}
```

**Response 409 — Email already registered**
```json
{
  "error": "already_registered",
  "msg": "email already registered, please log in"
}
```

> OTP is a 6-digit code valid for 10 minutes. Maximum 5 attempts per code. A new code can be requested after 30 seconds.

---

#### Step 2 — Verify OTP

```
POST /v1/auth/otp/verify
Content-Type: application/json
```

**Request**
```json
{
  "email": "user@example.com",
  "code": "483920"
}
```

**Response 200 — Verified**
```json
{
  "verificationToken": "a3f9c821b4d0e6f2..."
}
```

The `verificationToken` is a single-use opaque token valid for **10 minutes**. Pass it to `/v1/auth/register`.

**Error responses**

| HTTP | `error` | Meaning |
|------|---------|---------|
| 400 | `invalid_code` | Wrong code or no pending code |
| 410 | `code_expired` | Code expired, request a new one |
| 429 | `too_many_attempts` | 5 failed attempts, request a new code |

---

#### Step 3 — Register

```
POST /v1/auth/register
Content-Type: application/json
```

**Request**
```json
{
  "email": "user@example.com",
  "verificationToken": "a3f9c821b4d0e6f2...",
  "name": "Priya Sharma",
  "password": "mySecurePass123"
}
```

Password must be at least 8 characters.

**Response 200 — Session created**
```json
{
  "user": {
    "id": "3fa85f64-5717-4562-b3fc-2c963f66afa6",
    "email": "user@example.com",
    "isGuest": false,
    "displayName": "Priya Sharma"
  },
  "accessToken": "eyJhbGciOiJIUzI1NiIsInR...",
  "refreshToken": "f8c3de3d1fea7542e808..."
}
```

---

### 3.2 Login (Email + Password)

```
POST /v1/auth/login
Content-Type: application/json
```

**Request**
```json
{
  "email": "user@example.com",
  "password": "mySecurePass123"
}
```

**Response 200 — Session created**
```json
{
  "user": {
    "id": "3fa85f64-5717-4562-b3fc-2c963f66afa6",
    "email": "user@example.com",
    "isGuest": false,
    "displayName": "Priya Sharma"
  },
  "accessToken": "eyJhbGciOiJIUzI1NiIsInR...",
  "refreshToken": "f8c3de3d1fea7542e808..."
}
```

**Error responses**

| HTTP | `error` | Meaning |
|------|---------|---------|
| 401 | `invalid_credentials` | Wrong email or password |
| 429 | `too_many_attempts` | Account locked for 5 minutes (5 failed attempts) |

---

### 3.3 Guest Login

Creates a temporary anonymous session. Guests **cannot** sign BLE credentials or link voice assistants.

```
POST /v1/auth/guest
Content-Type: application/json
```

**Response 200**
```json
{
  "user": {
    "id": "7a8b9c0d-...",
    "isGuest": true,
    "displayName": "Guest"
  },
  "accessToken": "eyJhbGciOiJIUzI1NiIsInR...",
  "refreshToken": "eyJhbGciOiJIUzI1NiIsInR..."
}
```

Guest access tokens are valid for **30 days** (no short-lived/refresh cycle).

---

### 3.4 Refresh Token

Rotates the refresh token on every call (single-use). If a token is replayed, the entire session family is revoked.

```
POST /v1/auth/refresh
Content-Type: application/json
```

**Request**
```json
{
  "refreshToken": "f8c3de3d1fea7542e808..."
}
```

**Response 200**
```json
{
  "accessToken": "eyJhbGciOiJIUzI1NiIsInR...",
  "refreshToken": "9b1deb4d-3b7d-4bad-9bdd..."
}
```

**Response 401 — Expired or reused**
```json
{
  "error": "invalid_refresh",
  "msg": "session expired, please log in"
}
```

> **Token rotation:** Store both the new `accessToken` and `refreshToken` atomically. The old refresh token is invalidated immediately after use.

---

### 3.5 Logout

Revokes the entire session family (all rotations of the given refresh token).

```
POST /v1/auth/logout
Content-Type: application/json
```

**Request**
```json
{
  "refreshToken": "f8c3de3d1fea7542e808..."
}
```

**Response 204** — No content. Always succeeds even if the token is invalid.

---

## 4. Device APIs

All routes require `Authorization: Bearer <accessToken>`.

---

### 4.1 List User Devices

Returns all devices claimed by the authenticated user, with live online status and current datapoint values.

```
GET /v1/devices
Authorization: Bearer <accessToken>
```

**Response 200**
```json
[
  {
    "id": "did-abc123",
    "did": "did-abc123",
    "pid": "light-rgbcw",
    "name": "Living Room Light",
    "room": "Living Room",
    "type": "lighting",
    "icon": "lightbulb",
    "on": true,
    "offline": false,
    "metric": "75%",
    "dps": {
      "1": true,
      "2": 75,
      "3": 50,
      "5": {"r": 255, "g": 200, "b": 100}
    },
    "capabilities": [
      {"dp": "1", "kind": "power",      "label": "Power"},
      {"dp": "2", "kind": "brightness", "label": "Brightness", "min": 0,  "max": 100, "unit": "%"},
      {"dp": "3", "kind": "color_temp", "label": "Color Temperature", "min": 0, "max": 100},
      {"dp": "5", "kind": "color",      "label": "Color"}
    ]
  },
  {
    "id": "did-def456",
    "did": "did-def456",
    "pid": "sp1",
    "name": "Kitchen Plug",
    "room": "Kitchen",
    "type": "plug",
    "icon": "power_plug",
    "on": false,
    "offline": true,
    "metric": "Off",
    "dps": {"1": false},
    "capabilities": [
      {"dp": "1", "kind": "power", "label": "Power"}
    ]
  }
]
```

**DPS field:** `dps` key is the datapoint number as a string. Values are native JSON — boolean for power/lock, integer for brightness/temperature, object `{r,g,b}` for color.

**Tile metric:** `metric` is a human-readable status string for the app tile UI (`"75%"`, `"22°C"`, `"On"`, `"Off"`).

---

### 4.2 Claim Device (Virtual / Mock)

Creates a virtual device or links a pre-provisioned device by its `did`. Used for development/demo and for real provisioned devices that were set up via ZTP.

```
POST /v1/devices/claim
Authorization: Bearer <accessToken>
Content-Type: application/json
```

**Request**
```json
{
  "name": "Bedroom Fan",
  "room": "Bedroom",
  "type": "plug",
  "icon": "fan",
  "did": "optional-existing-did"
}
```

- `did` is optional. Omit it to create a mock device (generates a random `did`).
- `type` must be one of: `lighting` | `plug` | `climate` | `security` | `entertainment` | `sensors`

**Response 201**
```json
{
  "id": "did-abc123",
  "did": "did-abc123",
  "pid": "sp1",
  "name": "Bedroom Fan",
  "room": "Bedroom",
  "type": "plug",
  "icon": "fan",
  "on": false,
  "offline": true,
  "metric": "Off",
  "dps": {"1": false},
  "capabilities": [
    {"dp": "1", "kind": "power", "label": "Power"}
  ]
}
```

**Response 409** — Device already claimed by another user.

---

### 4.3 Adopt Device (Physical — After BLE Onboarding)

Links a physically provisioned device (already in inventory with a MAC address) to the authenticated user. Use this after the BLE onboarding flow where the device has been factory-provisioned.

```
POST /v1/devices/adopt
Authorization: Bearer <accessToken>
Content-Type: application/json
```

**Request**
```json
{
  "mac": "AABBCCDDEEFF",
  "name": "My Smart Bulb",
  "room": "Bedroom",
  "icon": "lightbulb"
}
```

- `mac` accepts any format (`AA:BB:CC`, `AABBCCDDEE`, etc.) — normalized internally.
- `room` defaults to `"Living Room"` if omitted.

**Response 201** — Same structure as Claim Device, but with live DPS values from the shadow.

**Response 404**
```json
{"error": "not_found", "msg": "device not provisioned or not in inventory"}
```

**Response 409** — Device already linked to another user account.

---

### 4.4 Send Command

Sends a set of datapoint values to a device. The command is persisted and published over MQTT. The shadow `desired` state is updated optimistically.

```
POST /v1/devices/{id}/command
Authorization: Bearer <accessToken>
Content-Type: application/json
```

**Request — toggle power**
```json
{
  "dps": {
    "1": true
  }
}
```

**Request — set brightness + color temperature**
```json
{
  "dps": {
    "1": true,
    "2": 80,
    "3": 70
  }
}
```

**Request — set RGB color**
```json
{
  "dps": {
    "5": {"r": 255, "g": 128, "b": 0}
  }
}
```

Color DP requires `r`, `g`, `b` each in range 0–255.

**Response 202 — Queued**
```json
{
  "id": "cmd-uuid-here",
  "status": "pending"
}
```

The command status transitions: `pending` → `acked_ok` | `acked_fail` when the device responds via MQTT. Track status via WebSocket events.

**Response 404**
```json
{"error": "device_not_found", "msg": "not yours or missing"}
```

---

### 4.5 Delete Device

Removes the device from the user's account. Does **not** factory reset the physical device.

```
DELETE /v1/devices/{id}
Authorization: Bearer <accessToken>
```

**Response 204** — No content.

---

## 5. BLE Provisioning

Used during the physical device onboarding flow to authenticate the app to the device over BLE.

### 5.1 Sign BLE Nonce

The device generates a random nonce and sends it over BLE. The app forwards it here; the cloud signs `device_id‖nonce‖role` with the tenant's P-256 private key. The device verifies the signature using its burned-in cloud public key.

```
POST /v1/ble/sign
Authorization: Bearer <accessToken>
Content-Type: application/json
```

**Request**
```json
{
  "device_id": "did-abc123",
  "nonce": "a3f9c8b2d0e1...",
  "role": "owner"
}
```

- `role` is `"owner"` (default) or `"user"`.
- Guests (`role: guest`) are rejected with 403.

**Response 200**
```json
{
  "sig": "3045022100f4e3d2c1b0a9...128 hex chars..."
}
```

`sig` is 128 hex characters — raw r‖s (64 bytes), each padded to 32 bytes big-endian. Feed this directly to `uECC_verify()` on the device.

**TOFU (Trust On First Use):** The first non-guest user to successfully sign for an unowned device auto-claims it. Ownership is based on physical BLE proximity as proof of possession.

**Response 403**
```json
{"error": "forbidden", "msg": "device already claimed by another user"}
```

---

## 6. Product Profiles

Returns the authoritative capability profile for a product ID. Clients use this to render the control UI dynamically. Response is cacheable.

```
GET /v1/products/{pid}/profile
Authorization: Bearer <accessToken>
```

**Supported PIDs:** `light-rgbcw` | `light1` | `th1` | `sp1` | `gen1`

**Response 200** *(example: `light-rgbcw`)*
```json
{
  "pid": "light-rgbcw",
  "consumer_type": "lighting",
  "display": {
    "icon": "lightbulb",
    "default_name": "Smart Light"
  },
  "capabilities": [
    {"dp": "1", "kind": "power",      "label": "Power"},
    {"dp": "2", "kind": "brightness", "label": "Brightness", "min": 0,  "max": 100, "unit": "%"},
    {"dp": "3", "kind": "color_temp", "label": "Color Temperature", "min": 0, "max": 100},
    {"dp": "5", "kind": "color",      "label": "Color"}
  ],
  "tile_metric": {
    "dp": "2",
    "format": "{value}%"
  }
}
```

**Response headers:** `Cache-Control: max-age=3600`, `ETag: "light-rgbcw"`. Supports conditional requests with `If-None-Match` → 304.

**Product catalog:**

| PID | Type | Capabilities |
|-----|------|-------------|
| `light-rgbcw` | lighting | power, brightness (0–100%), color_temp (0–100), color (RGB) |
| `light1` | lighting | power, brightness (10–100%) |
| `th1` | climate | power, target_temp (16–30°C) |
| `sp1` | plug | power |
| `gen1` | sensors | power |

---

## 7. Meta APIs

Static helper endpoints for building the app UI.

### 7.1 List Rooms

```
GET /v1/rooms
Authorization: Bearer <accessToken>
```

**Response 200**
```json
[
  {"name": "Living Room", "icon": "tv"},
  {"name": "Kitchen",     "icon": "plug"},
  {"name": "Bedroom",     "icon": "moon"}
]
```

### 7.2 List Scenes

```
GET /v1/scenes
Authorization: Bearer <accessToken>
```

**Response 200** — `[]` (stub, to be implemented)

### 7.3 Run Scene

```
POST /v1/scenes/{id}/run
Authorization: Bearer <accessToken>
```

**Response 204** — (stub)

### 7.4 List Automations

```
GET /v1/automations
Authorization: Bearer <accessToken>
```

**Response 200** — `[]` (stub)

### 7.5 Patch Automation

```
PATCH /v1/automations/{id}
Authorization: Bearer <accessToken>
```

**Response 204** — (stub)

---

## 8. Linked Accounts

Returns which voice platforms (Alexa, Google Home) the current user has linked via OAuth2.

```
GET /v1/linked-accounts
Authorization: Bearer <accessToken>
```

**Response 200**
```json
{
  "alexa":  true,
  "google": false
}
```

---

## 9. WebSocket

Real-time push for device state changes, online/offline events, and command acknowledgements.

### Connection

```
GET wss://api.setuiot.com/ws
Authorization: Bearer <accessToken>
```

Or via query parameter:
```
wss://api.setuiot.com/ws?token=<accessToken>
```

Filter to a single device:
```
wss://api.setuiot.com/ws?token=<accessToken>&did=<device-id>
```

The connection is **tenant-scoped** — you only receive events for devices belonging to the authenticated user's tenant.

### Event Envelope

Every message is a JSON object:

```json
{
  "type": "<event-type>",
  "tid":  "setu",
  "did":  "did-abc123",
  "t":    1750000000,
  "data": { ... }
}
```

### Event Types

#### `rpt` — Device Reported State

Fired when the device sends updated datapoint values.

```json
{
  "type": "rpt",
  "tid":  "setu",
  "did":  "did-abc123",
  "t":    1750000042,
  "data": {
    "1": true,
    "2": 75
  }
}
```

**Action:** Update the device tile's `dps` and re-derive `on`, `metric`.

---

#### `ack` — Command Acknowledged

Fired when the device responds to a command.

```json
{
  "type": "ack",
  "tid":  "setu",
  "did":  "did-abc123",
  "t":    1750000045,
  "data": {
    "ok": true
  }
}
```

`data.ok = false` means the device rejected the command.

---

#### `boo` — Device Reconnect (Boot)

Fired when the device reconnects after being offline.

```json
{
  "type": "boo",
  "tid":  "setu",
  "did":  "did-abc123",
  "t":    1750000010,
  "data": {
    "rssi":   -62,
    "fw_ver": "1.3.2"
  }
}
```

**Action:** Set device `offline = false`.

---

#### `reg` — Device Registered

Fired when a device registers for the first time after factory reset or firmware flash.

```json
{
  "type": "reg",
  "tid":  "setu",
  "did":  "did-abc123",
  "t":    1750000001,
  "data": {
    "rssi":   -55,
    "fw_ver": "1.3.2"
  }
}
```

---

#### `offline` — Device Went Offline

Fired immediately when MQTT detects disconnect (LWT or clean shutdown).

```json
{
  "type": "offline",
  "tid":  "setu",
  "did":  "did-abc123",
  "t":    1750001200
}
```

**Action:** Set device `offline = true`. No `data` field.

---

#### `ota_done` / `ota_err` — OTA Firmware Update

```json
{
  "type": "ota_done",
  "tid":  "setu",
  "did":  "did-abc123",
  "t":    1750002000
}
```

---

### Client → Server Messages

Currently the WebSocket is **receive-only** from the app perspective. The server reads messages but ignores content (used only for keep-alive / close detection).

### Reconnection

Implement exponential backoff on disconnect. Re-authenticate if the access token has expired — the WebSocket will return HTTP 401 on the upgrade request.

---

## 10. Platform / Tenant APIs

These APIs are for **server-to-server** or **IoT dashboard** use. They authenticate with a **tenant API key** rather than a user account.

### 10.1 Exchange API Key for JWT

```
POST /auth/token
Content-Type: application/json
```

**Request**
```json
{
  "api_key": "your-tenant-api-key"
}
```

**Response 200**
```json
{
  "token":      "eyJhbGciOiJIUzI1NiIsInR...",
  "expires_at": 1750086400
}
```

Tenant JWT is valid for **24 hours**.

---

### 10.2 List All Devices (Tenant)

```
GET /devices
Authorization: Bearer <tenantJwt>
```

**Response 200** — Array of device records:
```json
[
  {
    "TID":          "setu",
    "DID":          "did-abc123",
    "PID":          "light-rgbcw",
    "FWVersion":    "1.3.2",
    "IP":           "192.168.1.42",
    "RSSI":         -62,
    "IsOnline":     true,
    "RegisteredAt": "2025-01-10T09:00:00Z",
    "LastSeenAt":   "2026-06-20T08:30:00Z",
    "HWConfig":     null
  }
]
```

---

### 10.3 Get Device + Shadow (Tenant)

```
GET /devices/{did}
Authorization: Bearer <tenantJwt>
```

**Response 200**
```json
{
  "device": {
    "TID":       "setu",
    "DID":       "did-abc123",
    "PID":       "light-rgbcw",
    "IsOnline":  true,
    "FWVersion": "1.3.2"
  },
  "shadow": {
    "desired":  {"1": true, "2": 80},
    "reported": {"1": true, "2": 75}
  }
}
```

---

### 10.4 Issue Command (Tenant)

```
POST /devices/{did}/commands
Authorization: Bearer <tenantJwt>
Content-Type: application/json
```

**Request**
```json
{
  "type":    "set",
  "payload": {"1": true, "2": 100}
}
```

**Response 202**
```json
{
  "id":        "cmd-uuid",
  "status":    "pending",
  "issued_at": 1750000100
}
```

---

### 10.5 List Device Events (Tenant)

Returns last 100 events for a device.

```
GET /devices/{did}/events
Authorization: Bearer <tenantJwt>
```

**Response 200**
```json
[
  {
    "id":         1042,
    "tid":        "setu",
    "did":        "did-abc123",
    "event_type": "rpt",
    "payload":    {"1": true, "2": 75},
    "ts":         "2026-06-20T10:30:00Z"
  }
]
```

---

### 10.6 Health Check

```
GET /health
```

**Response 200**
```json
{
  "status": "ok",
  "db":     "ok",
  "redis":  "ok"
}
```

---

## 11. Factory ZTP API

Used by the **hardware factory** during device provisioning. Network-isolated; authenticated by a pre-shared factory token header.

```
POST /factory/provision
X-Factory-Token: <FACTORY_PROV_TOKEN>
Content-Type: application/json
```

**Request**
```json
{
  "mac": "AA:BB:CC:DD:EE:FF"
}
```

MAC is normalized (case-insensitive, any delimiter).

**Response 200 — Provision bundle**
```json
{
  "did":          "3fa85f64-5717-4562-b3fc-2c963f66afa6",
  "tid":          "setu",
  "pid":          "light-rgbcw",
  "mq_user":      "setu.3fa85f64",
  "mq_pass":      "device-mqtt-password",
  "mq_uri":       "mqtts://emqx.setuiot.com:8883",
  "cloud_pubkey": "a1b2c3d4e5f6...(128 hex chars = 64-byte P-256 X‖Y, uncompressed, NO 0x04 prefix)",
  "hw_config":    {"gpio_relay": 4, "gpio_led": 2}
}
```

This bundle is **burned into device flash** at the factory. The device uses `mq_user`/`mq_pass` to connect to MQTT and `cloud_pubkey` to verify signed BLE credentials.

**Error responses**

| HTTP | `error` | Meaning |
|------|---------|---------|
| 401 | `unauthorized` | Missing or wrong X-Factory-Token |
| 404 | `not_found` | MAC not in seeded inventory |
| 409 | `conflict` | Device already provisioned |

---

## 12. OAuth2 — Voice Assistant Account Linking

Enables Alexa and Google Home to control the user's devices. These endpoints follow the standard OAuth2 Authorization Code flow.

### 12.1 Authorization

```
GET  /oauth/authorize
POST /oauth/authorize
```

Used by Alexa / Google Home apps. Renders a login page (GET) or processes credentials (POST). On success, redirects to `redirect_uri?code=<auth_code>&state=<state>`.

Query parameters (GET):

| Param | Value |
|-------|-------|
| `client_id` | Alexa or Google client ID |
| `redirect_uri` | Platform-provided callback |
| `state` | Opaque state from platform |
| `scope` | `devices:read devices:control` |
| `response_type` | `code` |

---

### 12.2 Token Exchange

```
POST /oauth/token
Content-Type: application/x-www-form-urlencoded
Authorization: Basic <base64(client_id:client_secret)>
```

**Authorization Code → Tokens**
```
grant_type=authorization_code&code=<code>&redirect_uri=<uri>
```

**Refresh Token**
```
grant_type=refresh_token&refresh_token=<token>
```

**Response 200**
```json
{
  "access_token":  "abc123...",
  "token_type":    "Bearer",
  "expires_in":    3600,
  "refresh_token": "xyz789..."
}
```

---

### 12.3 User Info

Required by Alexa.

```
GET /oauth/userinfo
Authorization: Bearer <oauth-access-token>
```

**Response 200**
```json
{
  "sub":   "user-uuid",
  "email": "user@example.com",
  "name":  "Priya Sharma"
}
```

---

### 12.4 Revoke

```
POST /oauth/revoke
Content-Type: application/x-www-form-urlencoded

token=<access_or_refresh_token>
```

**Response 200** — Always succeeds.

---

### 12.5 Alexa Smart Home Skill

Handles all Alexa Smart Home directives.

```
POST /alexa/smarthome
Content-Type: application/json
```

Supported directives:
- `Alexa.Authorization/AcceptGrant` — skill enabled
- `Alexa.Discovery/Discover` — enumerate devices
- `Alexa/ReportState` — current device state
- `Alexa.PowerController/TurnOn`, `TurnOff`
- `Alexa.BrightnessController/SetBrightness`, `AdjustBrightness`
- `Alexa.ColorController/SetColor`
- `Alexa.ColorTemperatureController/SetColorTemperature`

---

### 12.6 Google Home Action

```
POST /google/smarthome
Content-Type: application/json
Authorization: Bearer <oauth-access-token>
```

Supported intents:
- `action.devices.SYNC` — discover devices
- `action.devices.QUERY` — query device state
- `action.devices.EXECUTE` — execute commands
- `action.devices.DISCONNECT` — account unlinked

---

## 13. MQTT Protocol — Device Side

> This section is for **firmware developers**. App developers do not interact with MQTT directly.

### Topic Structure

```
setu/{tid}/{pid}/{did}/dn   ← cloud-to-device (commands)
setu/{tid}/{pid}/{did}/up   ← device-to-cloud (events)
setu/{tid}/{pid}/{did}/shd  ← device shadow (retained, device publishes on connect)
```

Example: `setu/setu/light-rgbcw/3fa85f64-5717/up`

MQTT credentials are per-device (`mq_user = "{tid}.{did}"`). Devices only have ACL permission for their own topic subtree.

---

### Upstream Envelope (`/up`)

```json
{
  "v":   "1",
  "c":   "<command>",
  "id":  "<uuid>",
  "t":   1750000042,
  "pid": "light-rgbcw",
  "d":   { ... }
}
```

| Field | Type | Description |
|-------|------|-------------|
| `v` | string | Protocol version — always `"1"` |
| `c` | string | Command: `reg`, `boo`, `rpt`, `ack`, `offline`, `ota_done`, `ota_err` |
| `id` | string | UUID — for `ack`: same as the command ID in `/dn`; for others: device-generated |
| `t` | int64 | Unix timestamp (seconds) |
| `pid` | string | Product ID |
| `d` | object | Command-specific payload (see below) |

#### `reg` — First registration

```json
{"c": "reg", "d": {"rssi": -55, "fw_ver": "1.3.2"}}
```

#### `boo` — Boot / reconnect

```json
{"c": "boo", "d": {"rssi": -62, "fw_ver": "1.3.2"}}
```

#### `rpt` — Report datapoints

```json
{"c": "rpt", "d": {"1": true, "2": 75, "5": {"r": 255, "g": 200, "b": 100}}}
```

#### `ack` — Command acknowledgement

```json
{"c": "ack", "id": "<cmd-uuid-from-/dn>", "d": {"ok": true}}
```

#### `offline` — Clean shutdown

```json
{"c": "offline"}
```

Also configured as LWT (last will) so EMQX publishes it on unexpected disconnect.

---

### Downstream Envelope (`/dn`)

Published by cloud to device:

```json
{
  "v":  "1",
  "c":  "set",
  "id": "cmd-uuid",
  "t":  1750000040,
  "d":  {"1": true, "2": 80}
}
```

| `c` value | Description |
|-----------|-------------|
| `set` | Set datapoint values in `d` |
| `get` | Request the device to publish a full `/rpt` |
| `ota` | OTA firmware update trigger |

---

### Shadow Message (`/shd`, retained)

Published by device on every connect. Cloud stores this in Redis.

```json
{
  "v":      "1",
  "t":      1750000001,
  "online": true,
  "pid":    "light-rgbcw",
  "did":    "did-abc123",
  "rssi":   -55,
  "dps":    {"1": true, "2": 75}
}
```

The LWT shadow has `"online": false` so the cloud knows the device went offline even before the TCP timeout.

---

## 14. End-to-End Flows

### Flow 1 — New User Sign-Up

```
App                           Cloud                      Email
 │                              │                          │
 │  POST /v1/auth/otp/request   │                          │
 │──────────────────────────────►                          │
 │                              │  Send OTP email ─────────►
 │  {"sent": true}              │                    "Your code: 483920"
 │◄──────────────────────────────                          │
 │                              │                          │
 │  POST /v1/auth/otp/verify    │                          │
 │  {"email":"..","code":"483920"}                         │
 │──────────────────────────────►                          │
 │                              │                          │
 │  {"verificationToken":"..."}  │                          │
 │◄──────────────────────────────                          │
 │                              │                          │
 │  POST /v1/auth/register      │                          │
 │  {email, verificationToken,  │                          │
 │   name, password}            │                          │
 │──────────────────────────────►                          │
 │                              │                          │
 │  {user, accessToken,         │                          │
 │   refreshToken}              │                          │
 │◄──────────────────────────────                          │
```

---

### Flow 2 — Physical Device Onboarding (BLE + ZTP)

```
Factory                       Cloud                    Device (firmware)
   │                             │                          │
   │  POST /factory/provision    │                          │
   │  {mac: "AABBCCDDEEFF"}      │                          │
   │────────────────────────────►│                          │
   │                             │                          │
   │  {did, tid, pid, mq_user,   │                          │
   │   mq_pass, mq_uri,          │                          │
   │   cloud_pubkey, hw_config}  │                          │
   │◄────────────────────────────│                          │
   │                             │                          │
   │  [Burn into flash]          │                          │
   │──────────────────────────────────────────────────────►│
   │                             │                          │
   │                             │  MQTT connect            │
   │                             │◄─────────────────────────│
   │                             │  Publish /shd (retained) │
   │                             │◄─────────────────────────│
   │                             │  Publish /up reg         │
   │                             │◄─────────────────────────│
   │                             │                          │

App                           Cloud                    Device
  │                              │                          │
  │  POST /v1/devices/adopt      │                          │
  │  {mac, name, room, icon}     │                          │
  │──────────────────────────────►                          │
  │                              │                          │
  │  {deviceDTO with live dps}   │                          │
  │◄──────────────────────────────                          │
  │                              │                          │
  │  GET wss://.../ws            │                          │
  │  (open WebSocket)            │                          │
  │══════════════════════════════►                          │
```

---

### Flow 3 — BLE Credential Flow (App ↔ Device over Bluetooth)

```
App                           Cloud                    Device (BLE)
 │                              │                          │
 │  [Connect BLE]               │                          │
 │◄──────────────────────────────────────────────────────►│
 │                              │                          │
 │  [Read nonce from device]    │                    "Generate random nonce"
 │◄─────────────────────────────────────────────────────── │
 │                              │                          │
 │  POST /v1/ble/sign           │                          │
 │  {device_id, nonce, "owner"} │                          │
 │──────────────────────────────►                          │
 │                              │  Sign with P-256 key     │
 │  {"sig": "3045...128hex..."}  │                          │
 │◄──────────────────────────────                          │
 │                              │                          │
 │  [Write sig to BLE]          │                          │
 │───────────────────────────────────────────────────────►│
 │                              │                 uECC_verify(sig, cloud_pubkey)
 │                              │                 → Device grants owner access
```

---

### Flow 4 — Send Command & Receive Acknowledgement

```
App                           Cloud                    Device
 │                              │                          │
 │  POST /v1/devices/{id}/command                          │
 │  {"dps": {"1": true, "2": 80}}                          │
 │──────────────────────────────►                          │
 │                              │  MQTT /dn publish        │
 │                              │  {"c":"set","id":"cmd1", │
 │                              │   "d":{"1":true,"2":80}} │
 │                              │─────────────────────────►│
 │  {"id":"cmd1","status":"pending"}                       │
 │◄──────────────────────────────                          │
 │                              │                          │
 │  [WebSocket open]            │                          │
 │══════════════════════════════►                          │
 │                              │  MQTT /up ack            │
 │                              │  {"c":"ack","id":"cmd1", │
 │                              │   "d":{"ok":true}}       │
 │                              │◄─────────────────────────│
 │  WS: {"type":"ack",          │                          │
 │        "did":"...","data":   │  Redis Pub/Sub           │
 │        {"ok":true}}          │──────────────────────────►
 │◄══════════════════════════════                          │
 │                              │                          │
 │  WS: {"type":"rpt",          │                          │
 │        "did":"...","data":   │  (device reports new state)
 │        {"1":true,"2":80}}    │                          │
 │◄══════════════════════════════                          │
```

---

### Flow 5 — Token Refresh

```
App                           Cloud
 │                              │
 │  [accessToken expired]       │
 │                              │
 │  POST /v1/auth/refresh       │
 │  {"refreshToken": "old..."}  │
 │──────────────────────────────►
 │                              │  Mark old refresh token consumed
 │                              │  Issue new access + refresh token
 │  {"accessToken": "new...",   │
 │   "refreshToken": "new..."}  │
 │◄──────────────────────────────
 │                              │
 │  [Store both tokens atomically]
 │  [Retry original request]    │
```

---

### Flow 6 — Real-Time Device State (WebSocket)

```
App                           Cloud                    Device
 │                              │                          │
 │  WSS /ws?token=<jwt>         │                          │
 │══════════════════════════════►                          │
 │                              │                          │
 │                              │  [Device changes state]  │
 │                              │  MQTT /up rpt            │
 │                              │  {"c":"rpt","d":{"1":false}}
 │                              │◄─────────────────────────│
 │                              │                          │
 │                              │  Redis PUBLISH           │
 │                              │  ws:events:setu          │
 │                              │──────────────────────────►
 │                              │                          │
 │  WS: {"type":"rpt",          │  Hub broadcasts          │
 │       "did":"...",           │◄─────────────────────────│
 │       "data":{"1":false}}    │                          │
 │◄══════════════════════════════                          │
 │                              │                          │
 │  [Update tile: offline=false,│                          │
 │   on=false, metric="Off"]    │                          │
```

---

## 15. Error Reference

### Standard Error Body

```json
{
  "error": "<code>",
  "msg":   "<human-readable description>"
}
```

### Error Codes

| HTTP | `error` | Context |
|------|---------|---------|
| 400 | `bad_request` | Missing/invalid fields |
| 400 | `invalid_code` | Wrong or missing OTP code |
| 400 | `invalid_token` | verificationToken invalid or already used |
| 400 | `invalid_json` | Malformed request body |
| 401 | `unauthorized` | Missing or invalid JWT |
| 401 | `invalid_credentials` | Wrong email/password |
| 401 | `invalid_refresh` | Refresh token expired, used, or revoked |
| 403 | `forbidden` | Insufficient permissions (e.g. guest trying to sign BLE) |
| 404 | `not_found` | Device or resource doesn't exist |
| 404 | `device_not_found` | Device not found or not owned by user |
| 409 | `already_registered` | Email already has an account |
| 409 | `conflict` | Device already claimed |
| 409 | `email_taken` | Email taken during registration |
| 410 | `code_expired` | OTP or verification token expired |
| 410 | `token_expired` | verificationToken expired |
| 429 | `too_many_attempts` | OTP attempts or login lockout |
| 500 | `internal` | Unexpected server error |
| 500 | `mqtt_publish_failed` | Command accepted but MQTT delivery failed |

---

## 16. Data Types & Capability Kinds

### Capability Kinds

| Kind | DP value type | Range / Notes |
|------|--------------|---------------|
| `power` | `boolean` | `true` = on, `false` = off |
| `brightness` | `integer` | 0–100 (%) |
| `color_temp` | `integer` | 0–100 (maps to warm→cool Kelvin in adapters) |
| `color` | `{"r":int,"g":int,"b":int}` | each channel 0–255 |
| `target_temp` | `number` | °C, e.g. 16–30 |
| `thermo_mode` | `string` | `"auto"` \| `"cool"` \| `"heat"` \| `"off"` |
| `fan_speed` | `integer` | 1–N steps (product-specific) |
| `lock` | `boolean` | `true` = locked |
| `contact` | `boolean` | `true` = open |
| `motion` | `boolean` | `true` = motion detected |
| `humidity` | `integer` | 0–100 (%) |
| `temperature` | `number` | °C (read-only sensor) |

### Consumer Types

| `type` | Description |
|--------|-------------|
| `lighting` | Bulbs, strips, downlights |
| `plug` | Smart plugs, power strips |
| `climate` | Thermostats, AC controllers |
| `security` | Door sensors, cameras |
| `sensors` | Temperature, humidity, motion sensors |
| `entertainment` | TV, speakers |

### Token Lifetimes

| Token | TTL |
|-------|-----|
| Access token (app user) | 15 minutes |
| Refresh token (app user) | 60 days (sliding, rotated on use) |
| Guest access token | 30 days |
| OTP code | 10 minutes |
| Verification token | 10 minutes (post-OTP verify) |
| Tenant JWT | 24 hours |
| OAuth2 access token (voice) | 1 hour |

---

*Document generated from setu-cloud source — 2026-06-20*
