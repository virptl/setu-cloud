#!/bin/bash
# Publish /dn test commands to the WB3L device (no app). No "t" field => never
# treated as stale. Usage:
#   setu_dn.sh on|off
#   setu_dn.sh rgb R G B          e.g. setu_dn.sh rgb 255 0 0
#   setu_dn.sh bright N           0-100 (DP2)
#   setu_dn.sh temp N             0-100 (DP3)
#   setu_dn.sh get                ask device to republish shadow
#   setu_dn.sh cfg R G B [freq]   repoint RGB gpios live
#   setu_dn.sh ota URL [SIG] [EPOCH]   OTA (SIG=128-hex ES256; EPOCH=anti-rollback
#                                      counter; both required unless firmware is
#                                      built -DSETU_OTA_INSECURE=1)
#   setu_dn.sh raw JSON           send any envelope
#   setu_dn.sh watch              subscribe to up+shd to see replies
cd "$(dirname "$0")/.." || exit 1
U=$(grep -E "^MQTT_USERNAME=" .env | cut -d= -f2-)
P=$(grep -E "^MQTT_PASSWORD=" .env | cut -d= -f2-)
CID="2c3e7e79-d350-47a1-96dd-ce7a26ff1025"
BASE="setu/tid_5ea2bc8b4eac49908c8084f7a0315a9d/dh80dcnhpsm8mlwy/${CID}"
ID="t$(date +%s)"
pub() { mosquitto_pub -h 127.0.0.1 -p 1883 -u "$U" -P "$P" -q 1 -t "${BASE}/dn" -m "$1"; echo "sent -> $1"; }
case "$1" in
  on)     pub "{\"v\":\"1\",\"id\":\"$ID\",\"c\":\"set\",\"d\":{\"1\":true}}" ;;
  off)    pub "{\"v\":\"1\",\"id\":\"$ID\",\"c\":\"set\",\"d\":{\"1\":false}}" ;;
  rgb)    pub "{\"v\":\"1\",\"id\":\"$ID\",\"c\":\"set\",\"d\":{\"1\":true,\"5\":{\"r\":$2,\"g\":$3,\"b\":$4}}}" ;;
  bright) pub "{\"v\":\"1\",\"id\":\"$ID\",\"c\":\"set\",\"d\":{\"2\":$2}}" ;;
  temp)   pub "{\"v\":\"1\",\"id\":\"$ID\",\"c\":\"set\",\"d\":{\"3\":$2}}" ;;
  get)    pub "{\"v\":\"1\",\"id\":\"$ID\",\"c\":\"get\"}" ;;
  ota)    D="\"url\":\"$2\""
          [ -n "$3" ] && D="$D,\"sig\":\"$3\""
          [ -n "$4" ] && D="$D,\"epoch\":$4"
          pub "{\"v\":\"1\",\"id\":\"$ID\",\"c\":\"ota\",\"d\":{$D}}" ;;
  cfg)    F=${5:-5000}; pub "{\"v\":\"1\",\"id\":\"$ID\",\"c\":\"cfg\",\"d\":{\"hw_config\":{\"config\":{\"dps\":{\"1\":{\"type\":\"relay\",\"gpio\":4,\"active_high\":true},\"5\":{\"type\":\"rgb\",\"gpio_r\":$2,\"gpio_g\":$3,\"gpio_b\":$4,\"freq_hz\":$F,\"resolution\":8}}}}}}" ;;
  raw)    pub "$2" ;;
  watch)  echo "subscribing to ${BASE}/up and /shd (Ctrl-C to stop)..."; mosquitto_sub -h 127.0.0.1 -p 1883 -u "$U" -P "$P" -v -t "${BASE}/up" -t "${BASE}/shd" ;;
  *)      echo "usage: on|off|rgb R G B|bright N|temp N|get|ota URL [SIG] [EPOCH]|cfg R G B [freq]|raw JSON|watch"; exit 1 ;;
esac
