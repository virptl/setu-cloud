# setu-cloud VM Setup Guide

Target: Ubuntu/Debian VM (srv1661982 — 187.127.166.16)

---

## 1. Install System Dependencies

```bash
apt update && apt install -y postgresql redis-server
snap install go --classic
```

Add Go binaries to PATH (add to `~/.bashrc` for persistence):
```bash
export PATH=$PATH:$(go env GOPATH)/bin
```

---

## 2. PostgreSQL — Create DB and User

```bash
sudo -u postgres psql << 'EOF'
CREATE USER setu WITH PASSWORD 'setu_pass';
CREATE DATABASE setucore OWNER setu;
GRANT ALL PRIVILEGES ON DATABASE setucore TO setu;
EOF
```

Verify:
```bash
psql -U setu -d setucore -h localhost -c '\l'
```

---

## 3. Redis — Enable and Start

```bash
systemctl enable redis-server
systemctl start redis-server
systemctl status redis-server   # should show: active (running)
```

---

## 4. Clone and Configure

```bash
cd /root/viral
git clone git@github.com:virptl/setu-cloud.git
cd setu-cloud

cp .env.example .env
# Edit .env with real values (see section 5 below)
nano .env
```

---

## 5. Environment Variables (`.env`)

| Variable | Value | Notes |
|---|---|---|
| `LISTEN_ADDR` | `:8080` | HTTP server port |
| `DATABASE_URL` | `postgres://setu:setu_pass@localhost:5432/setucore?sslmode=disable` | Created in step 2 |
| `REDIS_URL` | `redis://localhost:6379` | Started in step 3 |
| `JWT_SECRET` | `0d48515532d69833e9bee36d8f118a9283e433a24f5ce6e7a9f9c17edb49eb66` | Generate new: `openssl rand -hex 32` |
| `MQTT_BROKER_URL` | `mqtts://187.127.166.16:8883` | EMQX on this VM |
| `MQTT_CLIENT_ID` | `setu-cloud` | |
| `MQTT_USERNAME` | `cloud_backend` | EMQX user (see section 6) |
| `MQTT_PASSWORD` | `SetuCloud2026!` | |
| `MQTT_CA_CERT_FILE` | `/root/certs/ca.crt` | SetuCore-Edge-Root-CA |
| `DEVICE_MQTT_BROKER_URI` | `mqtts://187.127.166.16:8883` | URI sent to devices at ZTP |
| `FACTORY_PROV_TOKEN` | `1f372790aadb44503d09fd1129788d72b1674ccc08332d8b36f8bbe210bafd08` | Must match firmware `CONFIG_SETU_FACTORY_PROV_TOKEN` |
| `CLOUD_PUBKEY_HEX` | *(empty for now)* | ES256 pub key — fill when keypair is generated |

---

## 6. EMQX — Create Cloud Backend User

The `cloud_backend` EMQX user gives setu-cloud wildcard subscribe/publish access.
Device credentials (`{tid}.{did}`) are separate and provisioned per-device via ZTP.

Create via EMQX Dashboard (`http://187.127.166.16:18083`):
- **Access Control → Authentication → Built-in Database → Users → Create**
- Username: `cloud_backend`
- Password: `SetuCloud2026!`
- Superuser: **enabled** (skips ACL checks)

> API key auth: create key at **System → API Keys**, then use with curl if needed.

---

## 7. Install goose and Run Migrations

```bash
go install github.com/pressly/goose/v3/cmd/goose@latest
export PATH=$PATH:$(go env GOPATH)/bin

cd /root/viral/setu-cloud
make migrate-up
```

Expected output: 6 migrations applied (`0001_tenants` through `0006_device_inventory`).

Check status:
```bash
make migrate-status
```

---

## 8. Build and Run

```bash
go mod tidy        # generates go.sum, downloads dependencies
go build ./...     # verify compilation

make run           # builds and starts the server on :8080
```

Verify the server is up:
```bash
curl http://localhost:8080/health
```

---

## 9. Sync from Git (after local changes)

```bash
cd /root/viral/setu-cloud
git pull origin master
make migrate-up    # apply any new migrations
make run
```

---

## Troubleshooting

**`make migrate-up` fails with "role does not exist"**
The Makefile auto-loads `.env` via `-include .env`. If `DATABASE_URL` is still empty,
source it manually: `export $(grep -v '^#' .env | xargs) && make migrate-up`

**MQTT TLS handshake fails**
Check that `MQTT_CA_CERT_FILE` points to the SetuCore-Edge-Root-CA cert at `/root/certs/ca.crt`.
Verify with: `openssl s_client -connect 187.127.166.16:8883 -CAfile /root/certs/ca.crt`

**Redis connection refused**
`systemctl start redis-server`

**PostgreSQL auth failed**
Confirm the `setu` user exists: `sudo -u postgres psql -c '\du'`
