#!/usr/bin/env python3
import os
import sys
import json
import time
import urllib.request
import urllib.parse
import jwt

def refresh_token(service_account_path, env_path=None):
    if not os.path.exists(service_account_path):
        print(f"Error: Service account file not found at {service_account_path}", file=sys.stderr)
        sys.exit(1)

    try:
        with open(service_account_path, "r") as f:
            sa_data = json.load(f)
    except Exception as e:
        print(f"Error reading service account file: {e}", file=sys.stderr)
        sys.exit(1)

    private_key = sa_data.get("private_key")
    client_email = sa_data.get("client_email")
    token_url = sa_data.get("token_uri", "https://oauth2.googleapis.com/token")

    if not private_key or not client_email:
        print("Error: Invalid service account JSON key format.", file=sys.stderr)
        sys.exit(1)

    now = int(time.time())
    payload = {
        "iss": client_email,
        "scope": "https://www.googleapis.com/auth/homegraph",
        "aud": token_url,
        "exp": now + 3600,
        "iat": now
    }

    try:
        # Sign the JWT using RS256
        assertion = jwt.encode(payload, private_key, algorithm="RS256")
    except Exception as e:
        print(f"Error signing JWT: {e}", file=sys.stderr)
        sys.exit(1)

    # Decode if bytes (older PyJWT returns bytes, newer returns str)
    if isinstance(assertion, bytes):
        assertion = assertion.decode("utf-8")

    # Request the access token from Google
    data = urllib.parse.urlencode({
        "grant_type": "urn:ietf:params:oauth:grant-type:jwt-bearer",
        "assertion": assertion
    }).encode("utf-8")

    req = urllib.request.Request(token_url, data=data, headers={"Content-Type": "application/x-www-form-urlencoded"})

    try:
        with urllib.request.urlopen(req) as resp:
            res_data = json.loads(resp.read().decode("utf-8"))
            access_token = res_data.get("access_token")
    except Exception as e:
        print(f"Error requesting access token from Google: {e}", file=sys.stderr)
        sys.exit(1)

    if not access_token:
        print("Error: Access token not found in Google response.", file=sys.stderr)
        sys.exit(1)

    print("Successfully fetched Google Service Account Token.")
    
    if env_path:
        update_env_file(env_path, access_token)
    else:
        print(f"\nGOOGLE_SA_TOKEN={access_token}\n")

def update_env_file(env_path, token):
    if not os.path.exists(env_path):
        print(f"Warning: .env file not found at {env_path}. Token not written to file.", file=sys.stderr)
        return

    try:
        with open(env_path, "r") as f:
            lines = f.readlines()
    except Exception as e:
        print(f"Error reading .env file: {e}", file=sys.stderr)
        return

    updated = False
    new_lines = []
    for line in lines:
        if line.strip().startswith("GOOGLE_SA_TOKEN="):
            new_lines.append(f"GOOGLE_SA_TOKEN={token}\n")
            updated = True
        else:
            new_lines.append(line)

    if not updated:
        # Append it if not present
        if new_lines and not new_lines[-1].endswith("\n"):
            new_lines[-1] = new_lines[-1] + "\n"
        new_lines.append(f"GOOGLE_SA_TOKEN={token}\n")

    try:
        with open(env_path, "w") as f:
            f.writelines(new_lines)
        print(f"Successfully updated GOOGLE_SA_TOKEN in {env_path}")
    except Exception as e:
        print(f"Error writing to .env file: {e}", file=sys.stderr)

if __name__ == "__main__":
    if len(sys.argv) < 2:
        print("Usage: python3 refresh_google_token.py <path_to_service_account_json> [path_to_env_file]")
        sys.exit(1)

    sa_path = sys.argv[1]
    env_path = sys.argv[2] if len(sys.argv) > 2 else None
    refresh_token(sa_path, env_path)
