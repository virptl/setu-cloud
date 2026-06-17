# setu-cloud — Consumer Module Build Spec (MVP)

**Audience:** a coding session with the `setu-cloud` repo checked out (branch `master`).
**Goal:** add the consumer-app layer so the Setu mobile app can: **(1)** register via
email-OTP, **(2)** add a **mock device** (no BLE), and **(3)** toggle it on/off so the
command is **published to the EMQX broker** and the round-trip state comes back.

This is additive. Do **not** change the existing tenant/device/ZTP machine API — the
consumer module sits beside it under a new `/v1` prefix and **reuses** the existing
MQTT publisher and shadow tables.

---

## 0. What already exists (reuse, don't reinvent)

Confirmed from the codebase:

| Thing | Location | Reuse for |
|---|---|---|
| chi router wiring | `internal/api/router.go` → `NewRouter(db, cache, pub, hub, cfg)` | mount new `/v1` routes |
| MQTT publish to device | `internal/mqtt/publisher.go` → `(*Publisher).Publish(tid,pid,did,cmdType,cmdID,d)` → topic `setu/{tid}/{pid}/{did}/dn` | the on/off → broker path |
| MQTT client (already connected to EMQX) | `internal/mqtt/client.go` → `NewClient(...)`; wired in `cmd/server/main.go` | publisher already shares this; mock device reuses `NewClient` |
| Inbound device ingest (`/up`,`/shd`) | `internal/mqtt/subscriber.go` + `internal/mqtt/router.go` | mock device's reports flow back through this automatically |
| Shadow store | `internal/shadow` + `shadows` table (`desired_value`/`reported_value` per dp) | device state for the app |
| JWT auth middleware | `internal/api/middleware/auth.go` → `Claims{TID,Role}`, `Auth(secret)` | extend with `UID`, add `AuthUser` |
| Postgres pool | `internal/db`, `pgxpool.Pool` | new tables |
| Config/env | `internal/config/config.go` | add consumer env vars |
| Migrations (goose) | `migrations/0001..0006` | add `0007` |

Module path: `github.com/setucore/setu-cloud`. Go 1.22. Libraries already present:
`go-chi/chi/v5`, `jackc/pgx/v5`, `golang-jwt/jwt/v5`, `eclipse/paho.mqtt.golang`,
`google/uuid`, `golang.org/x/crypto/bcrypt`, `pressly/goose/v3`.

---

## 1. Deliverables checklist

- [ ] `migrations/0007_consumer.sql` — `app_users`, `otp_codes`, `app_devices`; seed `setu` tenant
- [ ] `internal/config/config.go` — consumer env vars
- [ ] `internal/api/middleware/auth.go` — add `UID` to `Claims`; add `AuthUser` + `UIDFromContext`
- [ ] `internal/app/profiles.go` — device-type → capability profiles
- [ ] `internal/app/auth.go` — OTP request/verify, guest, logout, token issuing
- [ ] `internal/app/devices.go` — list, claim (add mock), command, delete; app device shape
- [ ] `internal/app/meta.go` — rooms / scenes / automations (rooms real, scenes/automations stubs)
- [ ] `internal/app/http.go` — small JSON/error helpers for the package
- [ ] `internal/api/router.go` — mount `/v1/*`
- [ ] `cmd/mockdevice/main.go` — fake device to **see** `/dn` and complete the round-trip
- [ ] Run migration, set env, `go build ./...`, verify with curl + app

---

## 2. Migration — `migrations/0007_consumer.sql`

> MVP keeps ownership flat (device → user). Homes/rooms tables are deferred (§14);
> `app_devices.room` is just a string. The `setu` tenant row is required because
> `devices.tid` has a FK to `tenants(tid)`.

```sql
-- +goose Up

-- Consumer tenant that owns all first-party app devices.
-- api_key_hash is a bcrypt of a random string; the app never uses tenant auth.
INSERT INTO tenants (tid, name, api_key_hash)
VALUES ('setu', 'Setu Consumer', '$2a$10$7EqJtq98hPqEX7fNZaFWoOhi5sfHc.RhVQ8e0m3a2k3aQv8m8Qb1m')
ON CONFLICT (tid) DO NOTHING;

CREATE TABLE app_users (
    id                UUID        PRIMARY KEY,
    email             TEXT        UNIQUE,            -- NULL for pure guests
    email_verified_at TIMESTAMPTZ,
    display_name      TEXT,
    is_guest          BOOLEAN     NOT NULL DEFAULT false,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE otp_codes (
    id          UUID        PRIMARY KEY,
    email       TEXT        NOT NULL,
    code_hash   TEXT        NOT NULL,                -- bcrypt of the 6-digit code
    expires_at  TIMESTAMPTZ NOT NULL,
    attempts    INT         NOT NULL DEFAULT 0,
    consumed_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_otp_email ON otp_codes (email, created_at DESC);

-- Maps an app user to a platform device (did under tenant 'setu').
CREATE TABLE app_devices (
    id          UUID        PRIMARY KEY,
    owner_id    UUID        NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    tid         TEXT        NOT NULL DEFAULT 'setu',
    did         TEXT        NOT NULL,
    pid         TEXT        NOT NULL,
    name        TEXT        NOT NULL,
    room        TEXT        NOT NULL DEFAULT 'Living Room',
    type        TEXT        NOT NULL,                -- lighting|plug|climate|security|entertainment|sensors
    icon        TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (did)
);
CREATE INDEX idx_app_devices_owner ON app_devices (owner_id);

-- +goose Down
DROP TABLE IF EXISTS app_devices;
DROP TABLE IF EXISTS otp_codes;
DROP TABLE IF EXISTS app_users;
```

> Replace the `api_key_hash` literal with any valid bcrypt string; it's never used.

---

## 3. Config — `internal/config/config.go`

Add fields to `Config` and load them in `Load()`:

```go
// Consumer module
ConsumerTID     string // tenant that owns app devices (default "setu")
OTPDevMode      bool   // true: log OTP to stdout and return it in the response
OTPTTLMinutes   int    // default 10
```

```go
ConsumerTID:   env("CONSUMER_TID", "setu"),
OTPDevMode:    env("OTP_DEV_MODE", "true") == "true",
OTPTTLMinutes: 10,
```

(Email sending is out of scope for MVP — `OTP_DEV_MODE=true` lets you test without SMTP.)

---

## 4. Auth middleware — `internal/api/middleware/auth.go`

Add `UID` to the existing `Claims` and a user-scoped middleware. Keep the existing
`Auth` untouched.

```go
type Claims struct {
    TID  string `json:"tid"`
    UID  string `json:"uid,omitempty"`
    Role string `json:"role"`
    jwt.RegisteredClaims
}

const uidKey contextKey = "uid"

// AuthUser validates a consumer (app user) JWT and injects tid + uid.
func AuthUser(secret string) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            raw := r.Header.Get("Authorization")
            if !strings.HasPrefix(raw, "Bearer ") {
                http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
                return
            }
            claims := &Claims{}
            _, err := jwt.ParseWithClaims(strings.TrimPrefix(raw, "Bearer "), claims,
                func(t *jwt.Token) (interface{}, error) {
                    if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
                        return nil, jwt.ErrSignatureInvalid
                    }
                    return []byte(secret), nil
                })
            if err != nil || claims.UID == "" {
                http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
                return
            }
            ctx := context.WithValue(r.Context(), tidKey, claims.TID)
            ctx = context.WithValue(ctx, uidKey, claims.UID)
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}

func UIDFromContext(ctx context.Context) string {
    uid, _ := ctx.Value(uidKey).(string)
    return uid
}
```

---

## 5. Package `internal/app`

### 5.1 `internal/app/http.go` — helpers

```go
package app

import "encoding/json"
import "net/http"

func writeJSON(w http.ResponseWriter, code int, v any) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(code)
    _ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, errCode, msg string) {
    writeJSON(w, code, map[string]string{"error": errCode, "msg": msg})
}
```

### 5.2 `internal/app/profiles.go` — capabilities per device type

The app renders its control sheet from `capabilities`. Map the wizard's `type` to a
`pid` and a capability set.

```go
package app

type Capability struct {
    DP    string `json:"dp"`
    Kind  string `json:"kind"`  // power | brightness | color_temp | target_temp
    Label string `json:"label"`
    Min   *int   `json:"min,omitempty"`
    Max   *int   `json:"max,omitempty"`
    Unit  string `json:"unit,omitempty"`
}

type profile struct {
    PID  string
    Caps []Capability
}

func ip(n int) *int { return &n }

// type -> pid + capabilities. dp "1" is always power.
func profileForType(t string) profile {
    power := Capability{DP: "1", Kind: "power", Label: "Power"}
    switch t {
    case "lighting":
        return profile{"light1", []Capability{power,
            {DP: "2", Kind: "brightness", Label: "Brightness", Min: ip(10), Max: ip(100), Unit: "%"}}}
    case "climate":
        return profile{"th1", []Capability{power,
            {DP: "2", Kind: "target_temp", Label: "Target Temperature", Min: ip(16), Max: ip(30), Unit: "°C"}}}
    case "plug":
        return profile{"sp1", []Capability{power}}
    default: // security | entertainment | sensors
        return profile{"gen1", []Capability{power}}
    }
}
```

### 5.3 `internal/app/auth.go` — email OTP, guest, logout

Endpoints (exact request/response in §11):

- `POST /v1/auth/otp/request` `{email}` → generate 6-digit code, bcrypt-hash it, insert
  into `otp_codes` (expires now+TTL). If `OTPDevMode`: log the code and include
  `"dev_code"` in the response. Always 200 (don't leak whether the email exists). Rate
  limit: reject if an unconsumed code was created < 30s ago.
- `POST /v1/auth/otp/verify` `{email, code}` → newest unconsumed, non-expired row for
  email; `attempts++`; bcrypt-compare; if ok mark `consumed_at`, upsert `app_users`
  (set `email_verified_at`), issue tokens, return session.
- `POST /v1/auth/guest` → create `app_users` row with `is_guest=true`, issue tokens.
- `POST /v1/auth/logout` → 204 (stateless JWT; no-op for MVP).

Token issuing helper (TID = ConsumerTID, role `user`/`guest`, 30-day exp for MVP):

```go
func issueToken(secret, tid, uid, role string) (string, error) {
    claims := &middleware.Claims{
        TID: tid, UID: uid, Role: role,
        RegisteredClaims: jwt.RegisteredClaims{
            ExpiresAt: jwt.NewNumericDate(time.Now().Add(30 * 24 * time.Hour)),
            IssuedAt:  jwt.NewNumericDate(time.Now()),
        },
    }
    return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}
```

Session response shape the app expects:

```go
type userDTO struct {
    ID          string `json:"id"`
    Email       string `json:"email,omitempty"`
    IsGuest     bool   `json:"isGuest"`
    DisplayName string `json:"displayName,omitempty"`
}
type session struct {
    User         userDTO `json:"user"`
    AccessToken  string  `json:"accessToken"`
    RefreshToken string  `json:"refreshToken"`
}
```

OTP request core (illustrative):

```go
func RequestOTP(db *pgxpool.Pool, cfg *config.Config) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        var body struct{ Email string `json:"email"` }
        if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Email == "" {
            writeErr(w, 400, "bad_request", "email required"); return
        }
        email := strings.ToLower(strings.TrimSpace(body.Email))

        // simple rate limit
        var recent int
        db.QueryRow(r.Context(),
            `SELECT count(*) FROM otp_codes WHERE email=$1 AND created_at > NOW()-interval '30 seconds'`,
            email).Scan(&recent)
        if recent > 0 { writeJSON(w, 200, map[string]bool{"sent": true}); return }

        code := fmt.Sprintf("%06d", rand.Intn(1000000))
        hash, _ := bcrypt.GenerateFromPassword([]byte(code), bcrypt.DefaultCost)
        db.Exec(r.Context(),
            `INSERT INTO otp_codes (id,email,code_hash,expires_at) VALUES ($1,$2,$3,$4)`,
            uuid.New(), email, string(hash),
            time.Now().Add(time.Duration(cfg.OTPTTLMinutes)*time.Minute))

        resp := map[string]any{"sent": true}
        if cfg.OTPDevMode {
            log.Printf("[OTP] %s -> %s", email, code)
            resp["dev_code"] = code
        }
        writeJSON(w, 200, resp)
    }
}
```

OTP verify core (illustrative):

```go
func VerifyOTP(db *pgxpool.Pool, cfg *config.Config) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        var body struct{ Email, Code string }
        json.NewDecoder(r.Body).Decode(&body)
        email := strings.ToLower(strings.TrimSpace(body.Email))

        var id, hash string
        var attempts int
        err := db.QueryRow(r.Context(), `
            SELECT id, code_hash, attempts FROM otp_codes
            WHERE email=$1 AND consumed_at IS NULL AND expires_at > NOW()
            ORDER BY created_at DESC LIMIT 1`, email).Scan(&id, &hash, &attempts)
        if err != nil { writeErr(w, 400, "invalid_code", "code expired or not found"); return }
        if attempts >= 5 { writeErr(w, 429, "too_many_attempts", "request a new code"); return }
        db.Exec(r.Context(), `UPDATE otp_codes SET attempts=attempts+1 WHERE id=$1`, id)
        if bcrypt.CompareHashAndPassword([]byte(hash), []byte(strings.TrimSpace(body.Code))) != nil {
            writeErr(w, 400, "invalid_code", "wrong code"); return
        }
        db.Exec(r.Context(), `UPDATE otp_codes SET consumed_at=NOW() WHERE id=$1`, id)

        // upsert user
        var uid, name string
        err = db.QueryRow(r.Context(), `SELECT id, COALESCE(display_name,'') FROM app_users WHERE email=$1`, email).Scan(&uid, &name)
        if err != nil {
            uid = uuid.New().String()
            name = strings.Split(email, "@")[0]
            db.Exec(r.Context(), `INSERT INTO app_users (id,email,email_verified_at,display_name) VALUES ($1,$2,NOW(),$3)`, uid, email, name)
        }
        tok, _ := issueToken(cfg.JWTSecret, cfg.ConsumerTID, uid, "user")
        writeJSON(w, 200, session{User: userDTO{ID: uid, Email: email, DisplayName: name}, AccessToken: tok, RefreshToken: tok})
    }
}
```

(Guest = same but `INSERT app_users(is_guest=true)`, `displayName="Guest"`, role `guest`.)

### 5.4 `internal/app/devices.go` — list / claim / command / delete

**App device shape returned to the mobile app** (must match exactly — the app maps `id`
for command/delete, and `capabilities` drives the control sheet):

```go
type deviceDTO struct {
    ID           string         `json:"id"`   // we use did as the public id
    DID          string         `json:"did"`
    Name         string         `json:"name"`
    Room         string         `json:"room"`
    Type         string         `json:"type"`
    Icon         string         `json:"icon"`
    On           bool           `json:"on"`
    Offline      bool           `json:"offline"`
    Metric       string         `json:"metric"`
    DPS          map[string]any `json:"dps"`
    Capabilities []Capability   `json:"capabilities"`
}
```

**GET `/v1/devices`** — list this user's devices, joined with online + reported shadow.

```go
func ListDevices(db *pgxpool.Pool) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        uid := middleware.UIDFromContext(r.Context())
        rows, _ := db.Query(r.Context(), `
            SELECT ad.did, ad.pid, ad.name, ad.room, ad.type, ad.icon,
                   COALESCE(d.is_online,false)
            FROM app_devices ad
            LEFT JOIN devices d ON d.tid=ad.tid AND d.did=ad.did
            WHERE ad.owner_id=$1 ORDER BY ad.created_at`, uid)
        defer rows.Close()

        out := []deviceDTO{}
        for rows.Next() {
            var did, pid, name, room, typ, icon string
            var online bool
            rows.Scan(&did, &pid, &name, &room, &typ, &icon, &online)
            dps := reportedDPS(r.Context(), db, "setu", did) // map[string]any from shadows
            on, _ := dps["1"].(bool)
            out = append(out, deviceDTO{
                ID: did, DID: did, Name: name, Room: room, Type: typ, Icon: icon,
                On: on, Offline: !online, Metric: metricFor(typ, on, dps),
                DPS: dps, Capabilities: profileForType(typ).Caps,
            })
        }
        writeJSON(w, 200, out)
    }
}
```

`reportedDPS` reads `SELECT dp_id, reported_value FROM shadows WHERE tid=$1 AND did=$2`
and returns a `map[string]any` (parse each `reported_value` JSON). `metricFor` is a small
helper ("On"/"Off"/"NN%"/"NN°C").

**POST `/v1/devices/claim`** — create the **mock device**. Body `{name, room, type, icon, did?}`.
If `did` omitted, generate one. Inserts a `devices` row (so the platform/subscriber accepts
its MQTT reports) and an `app_devices` row owned by this user. Returns the `deviceDTO`.

```go
func ClaimDevice(db *pgxpool.Pool, cfg *config.Config) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        uid := middleware.UIDFromContext(r.Context())
        var b struct{ Name, Room, Type, Icon, Did string }
        json.NewDecoder(r.Body).Decode(&b)
        if b.Type == "" || b.Name == "" { writeErr(w, 400, "bad_request", "name and type required"); return }
        prof := profileForType(b.Type)
        did := b.Did
        if did == "" { did = "mock" + uuid.New().String()[:8] }
        if b.Room == "" { b.Room = "Living Room" }

        // platform device row (idempotent), owned-by app_devices row
        if _, err := db.Exec(r.Context(),
            `INSERT INTO devices (tid,did,pid,is_online) VALUES ($1,$2,$3,false)
             ON CONFLICT (tid,did) DO NOTHING`, cfg.ConsumerTID, did, prof.PID); err != nil {
            writeErr(w, 500, "internal", err.Error()); return
        }
        if _, err := db.Exec(r.Context(),
            `INSERT INTO app_devices (id,owner_id,tid,did,pid,name,room,type,icon)
             VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
            uuid.New(), uid, cfg.ConsumerTID, did, prof.PID, b.Name, b.Room, b.Type, b.Icon); err != nil {
            writeErr(w, 409, "conflict", "device already claimed"); return
        }
        writeJSON(w, 201, deviceDTO{
            ID: did, DID: did, Name: b.Name, Room: b.Room, Type: b.Type, Icon: b.Icon,
            On: false, Offline: true, Metric: "Off", DPS: map[string]any{"1": false},
            Capabilities: prof.Caps,
        })
    }
}
```

**POST `/v1/devices/{id}/command`** — the on/off → broker path. Body `{dps:{"1":true,...}}`.
Authorize ownership, look up `pid`, **reuse `mqtt.Publisher.Publish`** with `cmdType="set"`,
write desired shadow, return 202.

```go
func Command(db *pgxpool.Pool, pub *mqtt.Publisher, cfg *config.Config) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        uid := middleware.UIDFromContext(r.Context())
        did := chi.URLParam(r, "id")
        var b struct{ DPS map[string]json.RawMessage `json:"dps"` }
        if err := json.NewDecoder(r.Body).Decode(&b); err != nil || len(b.DPS) == 0 {
            writeErr(w, 400, "bad_request", "dps required"); return
        }
        var pid string
        if err := db.QueryRow(r.Context(),
            `SELECT pid FROM app_devices WHERE did=$1 AND owner_id=$2`, did, uid).Scan(&pid); err != nil {
            writeErr(w, 404, "device_not_found", "not yours or missing"); return
        }
        cmdID := uuid.New().String()
        payload, _ := json.Marshal(b.DPS)
        db.Exec(r.Context(),
            `INSERT INTO commands (id,tid,did,command_type,payload,status) VALUES ($1,$2,$3,'set',$4,'pending')`,
            cmdID, cfg.ConsumerTID, did, payload)
        if err := pub.Publish(cfg.ConsumerTID, pid, did, "set", cmdID, payload); err != nil {
            writeErr(w, 500, "mqtt_publish_failed", err.Error()); return
        }
        // optimistic desired shadow
        for dp, val := range b.DPS {
            if n, err := strconv.Atoi(dp); err == nil {
                db.Exec(r.Context(), `
                    INSERT INTO shadows (tid,did,dp_id,desired_value) VALUES ($1,$2,$3,$4)
                    ON CONFLICT (tid,did,dp_id) DO UPDATE SET desired_value=$4, updated_at=NOW()`,
                    cfg.ConsumerTID, did, n, []byte(val))
            }
        }
        writeJSON(w, 202, map[string]any{"id": cmdID, "status": "pending"})
    }
}
```

**DELETE `/v1/devices/{id}`** — `DELETE FROM app_devices WHERE did=$1 AND owner_id=$2`
(optionally also delete the `devices` row); 204.

### 5.5 `internal/app/meta.go` — rooms / scenes / automations

The app loads all of these at startup (`Promise.all`), so each must return **200**.

- `GET /v1/rooms` → static for MVP:
  `[{"name":"Living Room","icon":"tv"},{"name":"Kitchen","icon":"plug"},{"name":"Bedroom","icon":"moon"}]`
- `GET /v1/scenes` → `[]`
- `GET /v1/automations` → `[]`
- `POST /v1/scenes/{id}/run` → 204 (stub)
- `PATCH /v1/automations/{id}` → 204 (stub)

---

## 6. Router wiring — `internal/api/router.go`

Add a `/v1` group using `AuthUser`. Auth endpoints are public; the rest require the user JWT.

```go
import "github.com/setucore/setu-cloud/internal/app"

// inside NewRouter, after existing routes:
r.Route("/v1", func(r chi.Router) {
    // public auth
    r.Post("/auth/otp/request", app.RequestOTP(db, cfg))
    r.Post("/auth/otp/verify",  app.VerifyOTP(db, cfg))
    r.Post("/auth/guest",       app.Guest(db, cfg))
    r.Post("/auth/logout",      app.Logout())

    // authenticated (app user JWT)
    r.Group(func(r chi.Router) {
        r.Use(middleware.AuthUser(cfg.JWTSecret))
        r.Get("/devices",              app.ListDevices(db))
        r.Post("/devices/claim",       app.ClaimDevice(db, cfg))
        r.Post("/devices/{id}/command", app.Command(db, pub, cfg))
        r.Delete("/devices/{id}",      app.DeleteDevice(db))
        r.Get("/rooms",                app.Rooms())
        r.Get("/scenes",               app.Scenes())
        r.Get("/automations",          app.Automations())
        r.Post("/scenes/{id}/run",     app.RunScene())
        r.Patch("/automations/{id}",   app.PatchAutomation())
    })
})
```

`NewRouter` already receives `db`, `pub`, and `cfg` — no signature change needed.

---

## 7. Mock device — `cmd/mockdevice/main.go`

This is how you **see** the broker receive on/off and complete the round-trip. It reuses
`internal/mqtt.NewClient`, subscribes to its `/dn` topic, logs every command, and reports
back on `/shd` (retained) + `/up`, so the device shows **online** and state updates in the app.

```go
package main

import (
    "encoding/json"
    "fmt"
    "log"
    "os"
    "os/signal"
    "syscall"
    "time"

    pahomqtt "github.com/eclipse/paho.mqtt.golang"
    "github.com/setucore/setu-cloud/internal/mqtt"
)

func main() {
    broker := env("MQTT_BROKER_URL", "tcp://localhost:1883")
    user := os.Getenv("MQTT_USERNAME")     // use cloud_backend for the demo (full setu/# access)
    pass := os.Getenv("MQTT_PASSWORD")
    ca := os.Getenv("MQTT_CA_CERT_FILE")
    tid := env("TID", "setu")
    pid := env("PID", "sp1")
    did := env("DID", "")
    if did == "" { log.Fatal("set DID=<the claimed device id>") }

    client, err := mqtt.NewClient(broker, "mock-"+did, user, pass, ca)
    if err != nil { log.Fatal(err) }

    state := map[string]any{"1": false}
    up := fmt.Sprintf("setu/%s/%s/%s/up", tid, pid, did)
    dn := fmt.Sprintf("setu/%s/%s/%s/dn", tid, pid, did)
    shd := fmt.Sprintf("setu/%s/%s/%s/shd", tid, pid, did)

    publishShadow := func() {
        b, _ := json.Marshal(map[string]any{"v": "1", "t": time.Now().Unix(),
            "online": true, "pid": pid, "did": did, "dps": state})
        client.Publish(shd, 1, true, b) // retained
    }

    client.Subscribe(dn, 1, func(_ pahomqtt.Client, m pahomqtt.Message) {
        log.Printf("◀ /dn  %s", string(m.Payload()))   // <-- the broker received the on/off
        var env struct {
            ID string                     `json:"id"`
            C  string                     `json:"c"`
            D  map[string]json.RawMessage `json:"d"`
        }
        json.Unmarshal(m.Payload(), &env)
        if env.C == "set" {
            for k, v := range env.D {
                var val any; json.Unmarshal(v, &val); state[k] = val
            }
            // ack + delta report + shadow
            ack, _ := json.Marshal(map[string]any{"v": "1", "c": "ack", "id": env.ID,
                "t": time.Now().Unix(), "pid": pid, "d": map[string]bool{"ok": true}})
            client.Publish(up, 1, false, ack)
            rpt, _ := json.Marshal(map[string]any{"v": "1", "c": "rpt", "id": env.ID,
                "t": time.Now().Unix(), "pid": pid, "d": state})
            client.Publish(up, 0, false, rpt)
            publishShadow()
            log.Printf("▶ applied %v, reported back", state)
        }
    })

    // announce online (boot + retained shadow)
    boo, _ := json.Marshal(map[string]any{"v": "1", "c": "boo", "id": "boot",
        "t": time.Now().Unix(), "pid": pid, "d": map[string]any{"rssi": -55}})
    client.Publish(up, 1, false, boo)
    publishShadow()
    log.Printf("mock device online: tid=%s pid=%s did=%s — waiting for commands…", tid, pid, did)

    q := make(chan os.Signal, 1); signal.Notify(q, syscall.SIGINT, syscall.SIGTERM); <-q
}

func env(k, d string) string { if v := os.Getenv(k); v != "" { return v }; return d }
```

> **Demo shortcut:** run the mock device with the **`cloud_backend`** MQTT credentials (full
> `setu/#` access), so you don't need to create a per-device EMQX user/ACL. For a "real"
> device you'd add an EMQX user `setu.<did>` with the `${clientid}` ACL and set
> `clientid=did` (see `docs/EMQX_BROKER_SETUP.md`).

---

## 8. EMQX notes

- setu-cloud already connects to EMQX (the publisher shares that client). The consumer
  `Command` handler publishes through it — **no new broker config** for the app→broker path.
- TLS: the broker cert SAN is `IP:187.127.166.16` only. Connect using
  `MQTT_BROKER_URL=mqtts://187.127.166.16:8883` (matches the SAN) with
  `MQTT_CA_CERT_FILE=/root/certs/ca.crt`. (Or reissue the cert with a DNS SAN for
  `emqx.setuiot.com`.)
- To **watch** commands without the mock device, on the VPS:
  `emqx ctl ...` traces, or `mosquitto_sub --cafile /root/certs/ca.crt -h 187.127.166.16 -p 8883 -u cloud_backend -P <pass> -t 'setu/setu/#' -v`

---

## 9. Build & migrate

```bash
go build ./...                      # compiles server + cmd/mockdevice
goose -dir migrations postgres "$DATABASE_URL" up   # applies 0007
# (or however migrations are run in this repo / Makefile)
```

Env for the server (consumer additions on top of existing):
```env
CONSUMER_TID=setu
OTP_DEV_MODE=true        # OTP printed to logs + returned as dev_code
```

---

## 10. Verify with curl (the real flow)

```bash
BASE=http://localhost:8080

# 1) request OTP (dev mode returns the code)
curl -s $BASE/v1/auth/otp/request -H 'content-type: application/json' \
  -d '{"email":"alex@example.com"}'
# -> {"sent":true,"dev_code":"482913"}

# 2) verify -> get accessToken
TOKEN=$(curl -s $BASE/v1/auth/otp/verify -H 'content-type: application/json' \
  -d '{"email":"alex@example.com","code":"482913"}' | jq -r .accessToken)

# 3) claim a mock device (note the returned did)
DID=$(curl -s $BASE/v1/devices/claim -H "authorization: Bearer $TOKEN" \
  -H 'content-type: application/json' \
  -d '{"name":"Test Plug","room":"Kitchen","type":"plug","icon":"plug"}' | jq -r .did)

# 4) (optional) start the mock device so it reports back + shows online
DID=$DID MQTT_BROKER_URL=mqtts://187.127.166.16:8883 \
  MQTT_USERNAME=cloud_backend MQTT_PASSWORD=<pass> MQTT_CA_CERT_FILE=/root/certs/ca.crt \
  go run ./cmd/mockdevice &

# 5) turn it ON -> publishes to setu/setu/sp1/$DID/dn
curl -s -X POST $BASE/v1/devices/$DID/command -H "authorization: Bearer $TOKEN" \
  -H 'content-type: application/json' -d '{"dps":{"1":true}}'
# mock device logs: ◀ /dn {"v":"1","c":"set",...,"d":{"1":true}}

# 6) confirm state came back
curl -s $BASE/v1/devices -H "authorization: Bearer $TOKEN" | jq '.[0] | {name,on,offline}'
# -> {"name":"Test Plug","on":true,"offline":false}
```

**Success = step 5 prints in the mock-device log (broker received the command) and step 6
shows `on:true`.**

---

## 11. API contract reference (must match the mobile app exactly)

| Method | Path | Auth | Body | Response |
|---|---|---|---|---|
| POST | `/v1/auth/otp/request` | – | `{email}` | `200 {sent:true, dev_code?}` |
| POST | `/v1/auth/otp/verify` | – | `{email, code}` | `200 {user:{id,email,isGuest,displayName}, accessToken, refreshToken}` |
| POST | `/v1/auth/guest` | – | – | same session shape |
| POST | `/v1/auth/logout` | user | – | `204` |
| GET | `/v1/devices` | user | – | `200 deviceDTO[]` |
| POST | `/v1/devices/claim` | user | `{name,room,type,icon,did?}` | `201 deviceDTO` |
| POST | `/v1/devices/{id}/command` | user | `{dps:{"<dp>":value}}` | `202 {id,status}` |
| DELETE | `/v1/devices/{id}` | user | – | `204` |
| GET | `/v1/rooms` | user | – | `200 [{name,icon}]` |
| GET | `/v1/scenes` | user | – | `200 []` |
| GET | `/v1/automations` | user | – | `200 []` |
| POST | `/v1/scenes/{id}/run` | user | – | `204` |
| PATCH | `/v1/automations/{id}` | user | `{enabled}` | `204` |

`deviceDTO`: `{id, did, name, room, type, icon, on, offline, metric, dps, capabilities:[{dp,kind,label,min?,max?,unit?}]}`.

---

## 12. Point the app at setu-cloud

In the mobile app `src/config.ts`:
```ts
USE_MOCK: false,
API_BASE_URL: 'http://<vps-or-localhost>:8080',  // front with HTTPS for real devices
```
iOS Simulator → `http://localhost:8080`; Android emulator → `http://10.0.2.2:8080`.
The app already sends `Authorization: Bearer <accessToken>` on authenticated calls.

---

## 13. Acceptance criteria

1. App: email → OTP (from server log / `dev_code`) → lands on Home with no devices.
2. Add Device wizard → device appears on Home.
3. Toggling the device's power pill publishes to `setu/setu/<pid>/<did>/dn` — visible in the
   mock-device log (or `mosquitto_sub`).
4. With the mock device running, the tile reflects on/off and shows online after refresh.
5. Existing tenant/ZTP/device machine API still works unchanged.

---

## 14. Out of scope for this MVP (later)

- Homes / rooms tables + membership + family invites (devices are flat-owned for now).
- Home-scoped WebSocket fan-out (`/v1/stream`) — app is REST-only today; add for live updates.
- Real BLE pairing + cryptographic device claim (proof-of-possession).
- Email/SMS OTP delivery (currently dev-mode log); refresh-token rotation.
- Scenes/automations execution engine (stubs return empty).
- Per-device EMQX users/ACLs for non-mock devices.
```
