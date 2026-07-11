# scripts — WB3L bench/ops helpers

Test tooling for the WB3L (BK7231T) device against this cloud + its EMQX broker.
Run from the repo root on the cloud host; MQTT creds are read from `.env` at
runtime (no secrets committed). The device topic ids below target the current
bench unit (did `2c3e7e79-…-ce7a26ff1025`) — edit for another device.

| Script | Purpose |
|---|---|
| `ota_sign.sh <tid> <sha256hex>` | ES256-sign an OTA image hash with the tenant keystore key (wraps `cmd/ota-sign`). Prints `pub=` / `sig=`. |
| `setu_dn.sh …` | publish `/dn` test commands (`on/off/rgb/bright/temp/get/cfg/ota/raw/watch`) — drive the device without the app. |
| `push_rgb_cfg.sh` | wait for the device, then push the corrected RGB `config.dps` (GPIO 7/8/9) as a live `cfg`. |

Firmware-side runbook: `setu-firmware/docs/DEV_TESTING.md`. OTA details:
`setu-firmware/docs/OTA.md`.
