import asyncio
import os
import json
import hashlib
from bleak import BleakScanner, BleakClient
from cryptography.hazmat.primitives.kdf.hkdf import HKDF
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.ciphers.aead import AESGCM
from cryptography.hazmat.primitives.asymmetric import ec, utils
from cryptography.hazmat.backends import default_backend

TARGET_DEVICE_NAME = "Setu_Edge_Node"
BOND_FILE_PATH     = "setu_client_bond.json"
CLOUD_KEY_FILE     = "setu_cloud_key.json"

# ── Factory identity (permanent, never erased on reset) ───────────────────
# Sent once via factory_prov command after ES256 attestation.
# mq_user / mq_pass must match a valid credential in your EMQX built-in DB.
FACTORY_DID    = "dev001testdid01"   # Platform-assigned UUID — replace per device
FACTORY_TID    = "dev"
FACTORY_PID    = "sp1"
#FACTORY_MQ_URI = "mqtts://192.168.4.1:8883"
FACTORY_MQ_URI =  "mqtts://187.127.166.16:8883"
FACTORY_MQ_USER = "dev.dev001testdid01"   # Format: "{tid}.{did}"
FACTORY_MQ_PASS = "SetuMQTTPass123"

# ── Hardware profile (permanent, never erased on reset) ───────────────────
# Mirrors the Tuya DP schema. Each key is the DP ID (string). Supported types:
#   relay  — output actuator: gpio, active_high, start_on
#   button — physical input:  gpio, pullup, bind_dps (DP to toggle on press)
# This profile is for a single-channel smart plug with a physical button.
#
#HW_CONFIG = {
#    "config": {
#        "dps": {
#            "1": {"type": "relay",  "gpio": 12, "active_high": True,  "start_on": False},
#            "2": {"type": "button", "gpio": 0,  "pullup": True, "bind_dps": 1},
#        }
#    }
#}


HW_CONFIG = { "config": { "dps": {
    "1": { "type": "relay", "gpio": 12, "active_high": True, "initial_state": False },
    "2": { "type": "pwm",   "gpio": 13, "freq_hz": 5000, "resolution": 8, "initial_brightness": 0 },
    "3": { "type": "rgb",   "gpio_r": 14, "gpio_g": 15, "gpio_b": 16, "freq_hz": 5000, "resolution": 8 }
    }}}


# ── Wi-Fi credentials (erasable, sent via wifi_prov after hw_prov) ────────
WIFI_SSID = "Airtel_Virus"
WIFI_PASS = "Jay@54321"

SVC_UUID       = "1b058a00-bd91-4cf5-a818-f6cf01824707"
CHAR_91_WRITE  = "1b058a91-bd91-4cf5-a818-f6cf01824707"
CHAR_92_NOTIFY = "1b058a92-bd91-4cf5-a818-f6cf01824707"
CHAR_93_HSHAKE = "1b058a93-bd91-4cf5-a818-f6cf01824707"

# Load cloud signing key from file if present (written by rotate-key pipeline),
# otherwise fall back to the compiled-in test key that matches CLOUD_SERVER_PUB_KEY in firmware.
_FALLBACK_PRIV_KEY_HEX = "73d49f6c2e3a1b4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d"

def _load_cloud_signing_key():
    if os.path.exists(CLOUD_KEY_FILE):
        try:
            with open(CLOUD_KEY_FILE, "r") as f:
                d = json.load(f)
            key = ec.derive_private_key(int(d["private_key_hex"], 16), ec.SECP256R1(), default_backend())
            print(f"[+] Cloud signing key loaded from {CLOUD_KEY_FILE}.")
            return key
        except Exception as e:
            print(f"[!] Could not load {CLOUD_KEY_FILE}: {e}. Using fallback key.")
    return ec.derive_private_key(int(_FALLBACK_PRIV_KEY_HEX, 16), ec.SECP256R1(), default_backend())

server_private_key = _load_cloud_signing_key()

class LiveGcmBleClient:
    def __init__(self):
        self.private_key = ec.generate_private_key(ec.SECP256R1(), default_backend())
        self.public_key = self.private_key.public_key()
        self.aes_key = None
        self.nonce_base = None
        self.tx_seq = 0

        self.handshake_queue = asyncio.Queue()
        self.response_queue = asyncio.Queue()

    def get_raw_public_bytes(self):
        raw_pub = self.public_key.public_bytes(
            encoding=serialization.Encoding.X962,
            format=serialization.PublicFormat.UncompressedPoint
        )
        return raw_pub[1:]

    def derive_hkdf_keys(self, esp32_raw_pub_bytes):
        esp32_full = bytes([0x04]) + esp32_raw_pub_bytes
        esp_pub_key = ec.EllipticCurvePublicKey.from_encoded_point(ec.SECP256R1(), esp32_full)
        full_shared_secret = self.private_key.exchange(ec.ECDH(), esp_pub_key)
        raw_shared_secret = full_shared_secret[:32] if len(full_shared_secret) > 32 else full_shared_secret

        hkdf = HKDF(
            algorithm=hashes.SHA256(),
            length=28,
            salt=bytes(32),
            info=b"SETU_BLE_SESSION",
            backend=default_backend()
        )
        okm = hkdf.derive(raw_shared_secret)
        self.aes_key = okm[:16]
        self.nonce_base = okm[16:28]
        print(f"[*] Ephemeral GCM Keys Derived (Full Handshake). Key: {self.aes_key.hex()}")

    def derive_resumption_keys(self, psk_bytes, client_random_bytes, esp_random_bytes):
        """Derives fresh session keys via HKDF taking the long-term PSK as the secret."""
        combined_salt = client_random_bytes + esp_random_bytes
        hkdf = HKDF(
            algorithm=hashes.SHA256(),
            length=28,
            salt=combined_salt,
            info=b"SETU_BLE_SESSION",
            backend=default_backend()
        )
        okm = hkdf.derive(psk_bytes)
        self.aes_key = okm[:16]
        self.nonce_base = okm[16:28]
        print(f"[*] Ephemeral GCM Keys Derived (Fast Resumption). Key: {self.aes_key.hex()}")

    def decrypt_gcm_packet(self, raw_data):
        seq_bytes, nonce, payload = raw_data[:4], raw_data[4:16], raw_data[16:]
        aesgcm = AESGCM(self.aes_key)
        plaintext = aesgcm.decrypt(nonce, payload, seq_bytes)
        return json.loads(plaintext.decode('utf-8'))

    def encrypt_gcm_packet(self, payload_dict):
        self.tx_seq += 1
        seq_bytes = self.tx_seq.to_bytes(4, 'big')
        rand_bytes = os.urandom(12)
        nonce = bytes(a ^ b for a, b in zip(rand_bytes, self.nonce_base))
        plaintext = json.dumps(payload_dict).encode('utf-8')
        aesgcm = AESGCM(self.aes_key)
        ciphertext_with_tag = aesgcm.encrypt(nonce, plaintext, seq_bytes)
        return seq_bytes + nonce + ciphertext_with_tag

    def generate_cloud_signature(self, device_id, nonce_hex, role="owner"):
        signed_message_str = f"{device_id}{nonce_hex}{role}"
        message_bytes = signed_message_str.encode('utf-8')
        message_hash = hashlib.sha256(message_bytes).digest()
        der_sig = server_private_key.sign(message_hash, ec.ECDSA(utils.Prehashed(hashes.SHA256())))
        r, s = utils.decode_dss_signature(der_sig)
        return (r.to_bytes(32, 'big') + s.to_bytes(32, 'big')).hex()

    def handle_char93_notify(self, sender, data):
        self.handshake_queue.put_nowait(data)

    def handle_char92_notify(self, sender, data):
        self.response_queue.put_nowait(data)


async def _wait_for_status(client_engine, expected_status, timeout=5.0):
    """Drain the response queue until a frame with the expected status arrives.

    Unsolicited frames (e.g. button state events pushed by the device while it
    processes a command) are discarded with a log line so they cannot abort the
    provisioning flow by landing in the queue ahead of the real ACK.
    """
    deadline = asyncio.get_event_loop().time() + timeout
    while True:
        remaining = deadline - asyncio.get_event_loop().time()
        if remaining <= 0:
            raise asyncio.TimeoutError(f"Timed out waiting for status='{expected_status}'")
        encrypted = await asyncio.wait_for(client_engine.response_queue.get(), timeout=remaining)
        frame = client_engine.decrypt_gcm_packet(encrypted)
        if frame.get("status") == expected_status:
            return frame
        print(f"[~] Skipped unsolicited frame: status={frame.get('status', '?')} — waiting for '{expected_status}'")


async def run_provisioning_pipeline():
    print(f"[*] Scanning for target broadcasting as '{TARGET_DEVICE_NAME}'...")
    device = await BleakScanner.find_device_by_name(TARGET_DEVICE_NAME, timeout=10.0)

    if not device:
        print(f"[!] Target '{TARGET_DEVICE_NAME}' not found.")
        return

    print(f"[*] Found target: {device.name} [{device.address}]. Establishing connection...")
    client_engine = LiveGcmBleClient()

    # Load persistent bond if available
    persistent_bond = None
    if os.path.exists(BOND_FILE_PATH):
        try:
            with open(BOND_FILE_PATH, "r") as f:
                persistent_bond = json.load(f)
            print("[+] Persistent Bond File loaded! Arming Fast Session Resumption path.")
        except Exception as e:
            print(f"[!] Could not read bond file: {e}")

    async with BleakClient(device) as client:
        print("[*] Physical link established! Subscribing to pipelines...")
        await client.start_notify(CHAR_93_HSHAKE, client_engine.handle_char93_notify)
        await client.start_notify(CHAR_92_NOTIFY, client_engine.handle_char92_notify)

        # ======================================================================
        # PATH A: FAST SESSION RESUMPTION
        # ======================================================================
        if persistent_bond and "client_id" in persistent_bond and "psk" in persistent_bond:
            client_id_bytes = bytes.fromhex(persistent_bond["client_id"])
            psk_bytes = bytes.fromhex(persistent_bond["psk"])
            client_random = os.urandom(16)

            resumption_payload = client_id_bytes + client_random
            print(f"\n[>>>] Dispatching Fast Resumption Handshake (32B)...")
            await client.write_gatt_char(CHAR_93_HSHAKE, resumption_payload, response=True)

            print("[*] Waiting for ESP32 random contribution back on Char 93...")
            esp_random = await asyncio.wait_for(client_engine.handshake_queue.get(), timeout=5.0)

            client_engine.derive_resumption_keys(psk_bytes, client_random, esp_random)

            print("[*] Awaiting fast resumption confirmation from gatekeeper...")
            encrypted_confirmation = await asyncio.wait_for(client_engine.response_queue.get(), timeout=5.0)
            confirmation = client_engine.decrypt_gcm_packet(encrypted_confirmation)
            print(f"[<<<] Gatekeeper Reply:\n      {json.dumps(confirmation, indent=2)}")

            if confirmation.get("status") == "success":
                print("[+] SUCCESS: Session Resumed securely. Skipping attestation.")
            else:
                print("[!] Resumption rejected. Falling back to full pairing.")
                return

        # ======================================================================
        # PATH B: FULL ECDH PAIRING + ES256 CLOUD ATTESTATION
        # ======================================================================
        else:
            print("\n[!] No local bond stored. Initiating Full ECDH Handshake & Cloud Attestation...")
            app_pub_bytes = client_engine.get_raw_public_bytes()
            await client.write_gatt_char(CHAR_93_HSHAKE, app_pub_bytes, response=True)

            esp_pub_bytes = await asyncio.wait_for(client_engine.handshake_queue.get(), timeout=5.0)
            client_engine.derive_hkdf_keys(esp_pub_bytes)

            encrypted_challenge = await asyncio.wait_for(client_engine.response_queue.get(), timeout=5.0)
            challenge = client_engine.decrypt_gcm_packet(encrypted_challenge)

            sig = client_engine.generate_cloud_signature(challenge["device_id"], challenge["nonce"])
            auth_payload = {"cmd": "auth", "role": "owner", "sig": sig}

            print("[>>>] Pushing Cloud Attestation Token to Characteristic 91...")
            await client.write_gatt_char(CHAR_91_WRITE, client_engine.encrypt_gcm_packet(auth_payload), response=True)

            encrypted_confirmation = await asyncio.wait_for(client_engine.response_queue.get(), timeout=5.0)
            confirmation = client_engine.decrypt_gcm_packet(encrypted_confirmation)
            print(f"[<<<] Gatekeeper Reply:\n      {json.dumps(confirmation, indent=2)}")

            if confirmation.get("status") == "authorized":
                bond_data = {
                    "client_id": confirmation["client_id"],
                    "psk": confirmation["psk"]
                }
                with open(BOND_FILE_PATH, "w") as f:
                    json.dump(bond_data, f, indent=2)
                print(f"[+] SUCCESS: Persistent Bond committed to local JSON file [{BOND_FILE_PATH}]!")
            else:
                print("[!] Authorization rejected. Aborting.")
                return

        # ── Read device provisioning state from the confirmation ──────────────
        # The device embeds {"prov":{"factory":bool,"hw":bool,"wifi":bool}} in
        # every session confirmation so the app knows exactly which steps remain.
        prov = confirmation.get("prov", {})
        factory_done = prov.get("factory", False)
        hw_done      = prov.get("hw",      False)
        wifi_done    = prov.get("wifi",    False)

        print(f"\n[*] Device provisioning state: factory={factory_done}  hw={hw_done}  wifi={wifi_done}")

        if factory_done and hw_done and wifi_done:
            print("[+] Device is fully provisioned and online. Nothing to do.")
            return

        # ── Variables populated by factory_prov (needed for MQTT topic display) ─
        device_did = device_tid = device_pid = None

        # ======================================================================
        # STEP 1: FACTORY PROVISIONING (permanent — tid, pid, MQTT credentials)
        # ======================================================================
        if factory_done:
            print("\n[~] Factory identity already locked on device — skipping factory_prov.")
            # DID is always MAC-derived; tid/pid come from the locked factory config.
            # We don't know them locally so just use the configured constants as reference.
            device_did, device_tid, device_pid = None, FACTORY_TID, FACTORY_PID
        else:
            factory_payload = {
                "cmd":     "factory_prov",
                "did":     FACTORY_DID,
                "tid":     FACTORY_TID,
                "pid":     FACTORY_PID,
                "mq_user": FACTORY_MQ_USER,
                "mq_pass": FACTORY_MQ_PASS,
                "mq_uri":  FACTORY_MQ_URI,
            }

            # Include tenant public key if a rotated key file exists — provisions
            # the tenant's key atomically with factory identity (no separate BLE round-trip).
            if os.path.exists(CLOUD_KEY_FILE):
                try:
                    with open(CLOUD_KEY_FILE, "r") as f:
                        cloud_key_data = json.load(f)
                    factory_payload["cloud_pubkey"] = cloud_key_data["public_key_hex"]
                    print(f"      [+] Tenant cloud key attached from {CLOUD_KEY_FILE}.")
                except Exception as e:
                    print(f"      [!] Could not load cloud key file: {e}. Bootstrap key will remain.")

            print(f"\n[>>>] Dispatching Factory Provisioning Frame...")
            print(f"      tid={FACTORY_TID}  pid={FACTORY_PID}  broker={FACTORY_MQ_URI}")
            await client.write_gatt_char(CHAR_91_WRITE, client_engine.encrypt_gcm_packet(factory_payload), response=True)

            frame = await _wait_for_status(client_engine, "factory_prov_ok", timeout=5.0)
            print(f"[<<<] Device Reply:\n      {json.dumps(frame, indent=2)}")

            device_did = frame.get("did")
            device_tid = frame.get("tid")
            device_pid = frame.get("pid")
            print(f"\n[+] Factory identity stored on device:")
            print(f"    did={device_did}  tid={device_tid}  pid={device_pid}")
            print(f"    MQTT topics will be:")
            print(f"      setu/{device_tid}/{device_pid}/{device_did}/up")
            print(f"      setu/{device_tid}/{device_pid}/{device_did}/dn")
            print(f"      setu/{device_tid}/{device_pid}/{device_did}/shd")

        # ======================================================================
        # STEP 2: HARDWARE CONFIGURATION (permanent — DP/GPIO profile)
        # ======================================================================
        if hw_done:
            print("\n[~] Hardware profile already locked on device — skipping hw_prov.")
        else:
            hw_payload = {
                "cmd": "hw_prov",
                "hw":  HW_CONFIG,
            }

            print(f"\n[>>>] Dispatching Hardware Configuration Frame...")
            dp_count = len(HW_CONFIG["config"]["dps"])
            def _dp_gpio_label(v):
                t = v.get('type', '?')
                if t == 'rgb':
                    return f"{t}@GPIO{v.get('gpio_r','?')}/{v.get('gpio_g','?')}/{v.get('gpio_b','?')}"
                return f"{t}@GPIO{v.get('gpio', '?')}"
            print(f"      {dp_count} DP(s): " + ", ".join(
                f"DP{k}={_dp_gpio_label(v)}"
                for k, v in HW_CONFIG["config"]["dps"].items()
            ))
            await client.write_gatt_char(CHAR_91_WRITE, client_engine.encrypt_gcm_packet(hw_payload), response=True)

            frame = await _wait_for_status(client_engine, "hw_prov_ok", timeout=5.0)
            print(f"[<<<] Device Reply:\n      {json.dumps(frame, indent=2)}")
            print(f"[+] Hardware profile committed ({dp_count} DPs). GPIO layout is live.")

        # ======================================================================
        # STEP 3: WI-FI PROVISIONING (erasable — ssid + password only)
        # ======================================================================
        wifi_payload = {
            "cmd":  "wifi_prov",
            "ssid": WIFI_SSID,
            "pass": WIFI_PASS,
        }

        print(f"\n[>>>] Dispatching Wi-Fi Provisioning Frame (ssid={WIFI_SSID})...")
        await client.write_gatt_char(CHAR_91_WRITE, client_engine.encrypt_gcm_packet(wifi_payload), response=True)

        frame = await _wait_for_status(client_engine, "wifi_prov_ok", timeout=5.0)
        print(f"[<<<] Device Reply:\n      {json.dumps(frame, indent=2)}")
        print("[+] Wi-Fi credentials accepted. Device is connecting...")

        # ======================================================================
        # PHASE 4: LIVE TELEMETRY STREAM (connection confirmation + status)
        # ======================================================================
        print("\n[*] Listening for device responses (Ctrl+C to exit)...")

        try:
            while True:
                encrypted_frame = await asyncio.wait_for(client_engine.response_queue.get(), timeout=90.0)
                frame = client_engine.decrypt_gcm_packet(encrypted_frame)
                print(f"\n[<<<] Device Push:\n      {json.dumps(frame, indent=2)}")

                # connected = Wi-Fi + MQTT are up; provisioning is complete
                if frame.get("status") == "success" and frame.get("msg") == "connected":
                    print("\n[+] PROVISIONING COMPLETE — device is online and MQTT is active.")
                    print("[*] Check EMQX dashboard or run the subscriber command below:")
                    print(f"    mosquitto_sub -h 187.127.166.16 -p 8883 --cafile ca.crt \\")
                    print(f"      -u {FACTORY_MQ_USER} -P {FACTORY_MQ_PASS} \\")
                    print(f"      -t 'setu/{device_tid}/{device_pid}/+/#' -v")

        except asyncio.TimeoutError:
            print("\n[*] No further frames within 90-second window. Closing link.")
        except KeyboardInterrupt:
            print("\n[*] Interrupted by user.")

        print("\n[*] Closing secure link gracefully.")
        try:
            await client.stop_notify(CHAR_93_HSHAKE)
            await client.stop_notify(CHAR_92_NOTIFY)
        except Exception:
            pass  # device already closed the link (hidden mode transition)


async def run_wifi_reprov_pipeline():
    """Reconnect to an already-factory-provisioned device and update Wi-Fi credentials only.
    Used after a field reset (5x power cycle) which erases only nvs.net80211."""
    print(f"[*] Wi-Fi re-provisioning mode. Scanning for '{TARGET_DEVICE_NAME}'...")
    device = await BleakScanner.find_device_by_name(TARGET_DEVICE_NAME, timeout=10.0)

    if not device:
        print(f"[!] Target '{TARGET_DEVICE_NAME}' not found.")
        return

    client_engine = LiveGcmBleClient()

    persistent_bond = None
    if os.path.exists(BOND_FILE_PATH):
        try:
            with open(BOND_FILE_PATH, "r") as f:
                persistent_bond = json.load(f)
            print("[+] Bond file loaded — will attempt fast resumption.")
        except Exception as e:
            print(f"[!] Could not read bond file: {e}")

    async with BleakClient(device) as client:
        await client.start_notify(CHAR_93_HSHAKE, client_engine.handle_char93_notify)
        await client.start_notify(CHAR_92_NOTIFY, client_engine.handle_char92_notify)

        # Fast resumption (bond survives reset)
        if persistent_bond and "client_id" in persistent_bond and "psk" in persistent_bond:
            client_id_bytes = bytes.fromhex(persistent_bond["client_id"])
            psk_bytes = bytes.fromhex(persistent_bond["psk"])
            client_random = os.urandom(16)

            await client.write_gatt_char(CHAR_93_HSHAKE, client_id_bytes + client_random, response=True)
            esp_random = await asyncio.wait_for(client_engine.handshake_queue.get(), timeout=5.0)
            client_engine.derive_resumption_keys(psk_bytes, client_random, esp_random)

            confirmation = client_engine.decrypt_gcm_packet(
                await asyncio.wait_for(client_engine.response_queue.get(), timeout=5.0)
            )
            if confirmation.get("status") != "success":
                print("[!] Resumption failed. Device may need full re-attestation.")
                return
            print("[+] Session resumed via persistent bond.")
        else:
            print("[!] No bond file for fast resumption — full ECDH required first.")
            return

        # Send only wifi_prov (factory identity is already in NVS)
        wifi_payload = {"cmd": "wifi_prov", "ssid": WIFI_SSID, "pass": WIFI_PASS}
        print(f"\n[>>>] Sending Wi-Fi credentials (ssid={WIFI_SSID})...")
        await client.write_gatt_char(CHAR_91_WRITE, client_engine.encrypt_gcm_packet(wifi_payload), response=True)

        frame = await _wait_for_status(client_engine, "wifi_prov_ok", timeout=5.0)
        print(f"[<<<] {json.dumps(frame, indent=2)}")

        if frame.get("status") == "wifi_prov_ok":
            print("[+] Wi-Fi credentials dispatched. Waiting for connection...")
            try:
                while True:
                    resp = client_engine.decrypt_gcm_packet(
                        await asyncio.wait_for(client_engine.response_queue.get(), timeout=90.0)
                    )
                    print(f"\n[<<<] {json.dumps(resp, indent=2)}")
                    if resp.get("status") == "success" and resp.get("msg") == "connected":
                        print("\n[+] Device reconnected to Wi-Fi and MQTT successfully.")
                        break
            except asyncio.TimeoutError:
                print("\n[*] Timeout waiting for connection confirmation.")

        try:
            await client.stop_notify(CHAR_93_HSHAKE)
            await client.stop_notify(CHAR_92_NOTIFY)
        except Exception:
            pass


async def run_cloud_key_rotate_pipeline():
    """Generate a fresh P-256 key pair, push the public half to the device via BLE,
    and persist the private half to setu_cloud_key.json for use by the cloud/simulator.

    Security: the command is guarded by an authorized ES256 session — the device will
    reject it unless the caller can produce a valid signature under the CURRENT cloud key.
    After rotation the new key takes effect on the very next session handshake.
    """
    print(f"[*] Cloud key rotation mode. Scanning for '{TARGET_DEVICE_NAME}'...")
    device = await BleakScanner.find_device_by_name(TARGET_DEVICE_NAME, timeout=10.0)
    if not device:
        print(f"[!] Target '{TARGET_DEVICE_NAME}' not found.")
        return

    client_engine = LiveGcmBleClient()

    persistent_bond = None
    if os.path.exists(BOND_FILE_PATH):
        try:
            with open(BOND_FILE_PATH, "r") as f:
                persistent_bond = json.load(f)
        except Exception:
            pass

    async with BleakClient(device) as client:
        await client.start_notify(CHAR_93_HSHAKE, client_engine.handle_char93_notify)
        await client.start_notify(CHAR_92_NOTIFY, client_engine.handle_char92_notify)

        # Establish an authorized session (fast resumption or full ECDH)
        if persistent_bond and "client_id" in persistent_bond and "psk" in persistent_bond:
            client_id_bytes = bytes.fromhex(persistent_bond["client_id"])
            psk_bytes       = bytes.fromhex(persistent_bond["psk"])
            client_random   = os.urandom(16)

            await client.write_gatt_char(CHAR_93_HSHAKE, client_id_bytes + client_random, response=True)
            esp_random = await asyncio.wait_for(client_engine.handshake_queue.get(), timeout=5.0)
            client_engine.derive_resumption_keys(psk_bytes, client_random, esp_random)

            conf = client_engine.decrypt_gcm_packet(
                await asyncio.wait_for(client_engine.response_queue.get(), timeout=5.0)
            )
            if conf.get("status") != "success":
                print("[!] Resumption failed. Cannot rotate key without an authorized session.")
                return
            print("[+] Session resumed. Authorized to rotate cloud key.")
        else:
            print("\n[!] No bond file — initiating full ECDH + ES256 attestation...")
            app_pub_bytes = client_engine.get_raw_public_bytes()
            await client.write_gatt_char(CHAR_93_HSHAKE, app_pub_bytes, response=True)

            esp_pub_bytes = await asyncio.wait_for(client_engine.handshake_queue.get(), timeout=5.0)
            client_engine.derive_hkdf_keys(esp_pub_bytes)

            challenge = client_engine.decrypt_gcm_packet(
                await asyncio.wait_for(client_engine.response_queue.get(), timeout=5.0)
            )
            sig = client_engine.generate_cloud_signature(challenge["device_id"], challenge["nonce"])
            await client.write_gatt_char(CHAR_91_WRITE,
                client_engine.encrypt_gcm_packet({"cmd": "auth", "role": "owner", "sig": sig}),
                response=True)

            conf = client_engine.decrypt_gcm_packet(
                await asyncio.wait_for(client_engine.response_queue.get(), timeout=5.0)
            )
            if conf.get("status") != "authorized":
                print("[!] Authorization rejected. Cannot rotate key.")
                return

            bond_data = {"client_id": conf["client_id"], "psk": conf["psk"]}
            with open(BOND_FILE_PATH, "w") as f:
                json.dump(bond_data, f, indent=2)
            print("[+] Authorized.")

        # Generate a new P-256 key pair
        new_priv = ec.generate_private_key(ec.SECP256R1(), default_backend())
        new_pub_bytes = new_priv.public_key().public_bytes(
            encoding=serialization.Encoding.X962,
            format=serialization.PublicFormat.UncompressedPoint
        )[1:]  # strip 0x04 prefix → 64 raw bytes
        new_pub_hex = new_pub_bytes.hex()

        print(f"\n[*] Generated new P-256 key pair.")
        print(f"    New public key ({len(new_pub_bytes)*2} hex chars): {new_pub_hex[:32]}...{new_pub_hex[-8:]}")

        rotate_payload = {"cmd": "cloud_key_rotate", "data": {"pubkey": new_pub_hex}}
        print("\n[>>>] Dispatching cloud_key_rotate command...")
        await client.write_gatt_char(CHAR_91_WRITE,
            client_engine.encrypt_gcm_packet(rotate_payload), response=True)

        frame = client_engine.decrypt_gcm_packet(
            await asyncio.wait_for(client_engine.response_queue.get(), timeout=5.0)
        )
        print(f"[<<<] Device Reply:\n      {json.dumps(frame, indent=2)}")

        if frame.get("status") == "cloud_key_rotate_ok":
            # Derive private key scalar for persistence
            priv_scalar = new_priv.private_numbers().private_value
            key_data = {
                "private_key_hex": f"{priv_scalar:064x}",
                "public_key_hex":  new_pub_hex,
            }
            with open(CLOUD_KEY_FILE, "w") as f:
                json.dump(key_data, f, indent=2)
            print(f"\n[+] KEY ROTATION COMPLETE.")
            print(f"    New private key saved to {CLOUD_KEY_FILE}.")
            print(f"    The simulator and controller will use this key on next run.")
            print(f"\n    Action required: recompile firmware with updated CLOUD_SERVER_PUB_KEY_DEFAULT")
            print(f"    or the NVS key will be used automatically until the next full flash erase.")
        else:
            print(f"[!] Key rotation failed: {frame}")

        try:
            await client.stop_notify(CHAR_93_HSHAKE)
            await client.stop_notify(CHAR_92_NOTIFY)
        except Exception:
            pass


if __name__ == "__main__":
    import sys
    cmd = sys.argv[1] if len(sys.argv) > 1 else ""
    if cmd == "reprov":
        print("=== WI-FI RE-PROVISIONING (POST-RESET) ===")
        asyncio.run(run_wifi_reprov_pipeline())
    elif cmd == "rotate-key":
        print("=== CLOUD PUBLIC KEY ROTATION ===")
        asyncio.run(run_cloud_key_rotate_pipeline())
    else:
        print("=== FULL FACTORY + WI-FI PROVISIONING ORCHESTRATION ===")
        asyncio.run(run_provisioning_pipeline())
