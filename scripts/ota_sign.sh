#!/bin/bash
# Sign an OTA image hash with a tenant's cloud key (ES256, raw r||s hex).
# usage: scripts/ota_sign.sh <tid> <sha256-hex-of-firmware.uf2>
#   sha256sum firmware.uf2   # on the build machine, to get the hash
# Reads DATABASE_URL + KEY_ENCRYPTION_KEY from the repo .env.
cd "$(dirname "$0")/.." || exit 1
set -a; . ./.env; set +a
go run ./cmd/ota-sign "$@"
