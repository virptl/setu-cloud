#!/bin/bash
# Pushes the corrected RGB (GPIO 7/8/9) hw_config to the device as a live cfg
# command. Waits until the device is connected, then publishes (no "t" field so
# it is never treated as stale). Run AFTER flashing the config.dps firmware.
set -e
cd "$(dirname "$0")/.." || exit 1
U=$(grep -E "^MQTT_USERNAME=" .env | cut -d= -f2-)
P=$(grep -E "^MQTT_PASSWORD=" .env | cut -d= -f2-)
CID="2c3e7e79-d350-47a1-96dd-ce7a26ff1025"
TOPIC="setu/tid_5ea2bc8b4eac49908c8084f7a0315a9d/dh80dcnhpsm8mlwy/${CID}/dn"
PAYLOAD='{"v":"1","id":"cfgfix789","c":"cfg","d":{"hw_config":{"config":{"dps":{"1":{"type":"relay","gpio":4,"active_high":true},"5":{"type":"rgb","gpio_r":7,"gpio_g":8,"gpio_b":9,"freq_hz":5000,"resolution":8}}}}}}'
echo "Waiting for device ${CID} to connect..."
for i in $(seq 1 60); do
  if emqx_ctl clients show "$CID" 2>/dev/null | grep -q "connected=true"; then
    echo "Device online. Publishing cfg..."
    mosquitto_pub -h 127.0.0.1 -p 1883 -u "$U" -P "$P" -q 1 -t "$TOPIC" -m "$PAYLOAD"
    echo "Sent cfg to $TOPIC"
    exit 0
  fi
  sleep 2
done
echo "Timed out waiting for device to connect."; exit 1
