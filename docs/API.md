# setu-cloud API Reference

Base URL: `http://<host>:8080`

All authenticated endpoints require a JWT in the `Authorization` header:
```
Authorization: Bearer <token>
```

Tokens are tenant-scoped — a token issued for tenant A can only see tenant A's devices.

---

## Authentication

### POST /auth/token

Exchange a tenant API key for a JWT.

**Request**
```json
{
  "api_key": "your-tenant-api-key"
}
```

**Response `200`**
```json
{
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "expires_at": 1718400000
}
```
`expires_at` is a Unix timestamp (seconds). Tokens are valid for **24 hours**.

**Errors**
| Status | Body | Meaning |
|--------|------|---------|
| 400 | `{"error":"bad_request"}` | Missing or malformed body |
| 401 | `{"error":"unauthorized"}` | Invalid API key |

---

## Devices

### GET /devices

List all devices for the authenticated tenant.

**Response `200`**
```json
[
  {
    "TID": "tenant-uuid",
    "DID": "device-uuid",
    "PID": "product-uuid",
    "FWVersion": "1.2.3",
    "IP": "192.168.1.10",
    "RSSI": -65,
    "IsOnline": true,
    "RegisteredAt": "2024-01-15T10:00:00Z",
    "LastSeenAt": "2024-06-13T08:30:00Z",
    "HWConfig": { }
  }
]
```

Returns an empty array `[]` if no devices are registered.

---

### GET /devices/{did}

Get a single device along with its current shadow state.

**Path params**
| Param | Description |
|-------|-------------|
| `did` | Device UUID |

**Response `200`**
```json
{
  "device": {
    "TID": "tenant-uuid",
    "DID": "device-uuid",
    "PID": "product-uuid",
    "FWVersion": "1.2.3",
    "IP": "192.168.1.10",
    "RSSI": -65,
    "IsOnline": true,
    "RegisteredAt": "2024-01-15T10:00:00Z",
    "LastSeenAt": "2024-06-13T08:30:00Z",
    "HWConfig": { }
  },
  "shadow": {
    "desired": {
      "1": true,
      "2": 75
    },
    "reported": {
      "1": false,
      "2": 70
    }
  }
}
```

**Shadow fields:**
- `desired` — state the app has requested (set via commands)
- `reported` — last state the device actually reported
- Keys are datapoint IDs (dp_id) as strings; values are the datapoint values

**Errors**
| Status | Body | Meaning |
|--------|------|---------|
| 404 | `{"error":"not_found"}` | Device not found or not in this tenant |

---

### POST /devices/{did}/commands

Send a command to a device. The command is persisted and published to the device over MQTT.

**Path params**
| Param | Description |
|-------|-------------|
| `did` | Device UUID |

**Request**
```json
{
  "type": "set",
  "payload": {
    "1": true,
    "2": 75
  }
}
```

**Command types**
| `type` | Description |
|--------|-------------|
| `set` | Set one or more datapoints. Payload is `{ "<dp_id>": <value>, ... }`. Also updates desired shadow immediately. |
| `get` | Request the device to report its current state. Payload can be `null`. |
| `ota` | Trigger an OTA firmware update. Payload is product-specific. |

**Response `202`**
```json
{
  "id": "command-uuid",
  "status": "pending",
  "issued_at": 1718400000
}
```

The device will ACK the command asynchronously. Track acknowledgement via the WebSocket stream (see `ack` event below).

**Errors**
| Status | Body | Meaning |
|--------|------|---------|
| 400 | `{"error":"bad_request"}` | Missing or malformed body |
| 404 | `{"error":"device_not_found"}` | Device not found |
| 500 | `{"error":"mqtt_publish_failed"}` | Command saved but MQTT delivery failed |

---

### GET /devices/{did}/events

Retrieve the last 100 events for a device, newest first.

**Path params**
| Param | Description |
|-------|-------------|
| `did` | Device UUID |

**Response `200`**
```json
[
  {
    "id": 1042,
    "tid": "tenant-uuid",
    "did": "device-uuid",
    "event_type": "rpt",
    "payload": { "1": true, "2": 70 },
    "ts": "2024-06-13T08:30:00Z"
  }
]
```

**Event types**
| `event_type` | Triggered by |
|--------------|-------------|
| `reg` | Device first registration |
| `boo` | Device boot / reconnect |
| `rpt` | Device datapoint report |
| `ack` | Device acknowledged a command |
| `ota_done` | OTA update completed successfully |
| `ota_err` | OTA update failed |

---

## WebSocket — Real-time Events

### GET /ws

Upgrade to a WebSocket connection to receive real-time device events for the tenant.

**Authentication**

Pass the JWT either as a query parameter or header:
```
GET /ws?token=<jwt>
```
or
```
GET /ws
Authorization: Bearer <jwt>
```

**Optional filter**

To receive events for a single device only:
```
GET /ws?token=<jwt>&did=<device-uuid>
```

**Connection**

Once connected the server pushes JSON messages whenever a device event occurs. The client does not need to send anything.

**Message format**
```json
{
  "type": "rpt",
  "tid": "tenant-uuid",
  "did": "device-uuid",
  "t": 1718400000,
  "data": {
    "1": true,
    "2": 70
  }
}
```

| Field | Type | Description |
|-------|------|-------------|
| `type` | string | Event type (same values as event_type in the events endpoint) |
| `tid` | string | Tenant ID |
| `did` | string | Device ID that produced the event |
| `t` | number | Unix timestamp from the device |
| `data` | object | Event-specific payload |

**Event types over WebSocket**
| `type` | When it fires | `data` content |
|--------|--------------|----------------|
| `reg` | Device registers for the first time | `{ "rssi": -65, "fw_ver": "1.2.3" }` |
| `boo` | Device boots / reconnects | `{ "rssi": -65, "fw_ver": "1.2.3" }` |
| `rpt` | Device reports datapoint values | `{ "<dp_id>": <value>, ... }` |
| `ack` | Device acknowledges a command | `{ "ok": true }` — check `ok` to know if it succeeded |
| `ota_done` | OTA finished | Command-specific |
| `ota_err` | OTA failed | Command-specific |

**Reconnection**

The server drops slow clients silently. Implement exponential-backoff reconnection on the client side.

---

## Health

### GET /health

Returns `200 OK` when the server, database, and Redis are reachable. No authentication required. Useful for readiness checks.

---

## Error format

All error responses follow this shape:
```json
{
  "error": "error_code",
  "msg": "optional human-readable detail"
}
```

| HTTP status | Common `error` values |
|-------------|----------------------|
| 400 | `bad_request` |
| 401 | `unauthorized` |
| 404 | `not_found`, `device_not_found` |
| 409 | `conflict` |
| 500 | `internal`, `mqtt_publish_failed` |

