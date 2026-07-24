#!/usr/bin/env python3
import sys
import os
import bcrypt
import subprocess

def register_client(project_id, client_secret, db_url):
    # Google's redirect URI format
    redirect_uri = f"https://oauth-redirect.googleusercontent.com/r/{project_id}"
    
    # Hash the client secret using bcrypt
    hashed_secret = bcrypt.hashpw(client_secret.encode('utf-8'), bcrypt.gensalt(10)).decode('utf-8')
    
    # Use psql CLI command via subprocess
    sql_command = f"""
    INSERT INTO oauth_clients (client_id, client_secret, redirect_uris, name)
    VALUES ('google-smart-home', '{hashed_secret}', ARRAY['{redirect_uri}'], 'Google Home')
    ON CONFLICT (client_id) DO UPDATE 
    SET client_secret = EXCLUDED.client_secret,
        redirect_uris = EXCLUDED.redirect_uris,
        name = EXCLUDED.name;
    """
    
    try:
        process = subprocess.Popen(
            ['psql', db_url, '-c', sql_command],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE
        )
        stdout, stderr = process.communicate()
        if process.returncode != 0:
            raise Exception(stderr.decode('utf-8'))
            
        print(f"Successfully registered Google Smart Home client in database.")
        print(f"Client ID: google-smart-home")
        print(f"Redirect URI configured: {redirect_uri}")
    except Exception as e:
        print(f"Error registering client in DB: {e}", file=sys.stderr)
        sys.exit(1)

if __name__ == "__main__":
    db_url = os.environ.get("DATABASE_URL")
    if not db_url:
        # Try loading from .env
        if os.path.exists(".env"):
            with open(".env") as f:
                for line in f:
                    if line.strip().startswith("DATABASE_URL="):
                        db_url = line.strip().split("=", 1)[1]
                        # Remove quotes if present
                        if db_url.startswith('"') and db_url.endswith('"'):
                            db_url = db_url[1:-1]
                        elif db_url.startswith("'") and db_url.endswith("'"):
                            db_url = db_url[1:-1]
                        break

    if not db_url:
        db_url = "postgres://setu:setu_pass@localhost:5432/setucore?sslmode=disable"

    if len(sys.argv) < 3:
        print("Usage: python3 scripts/register_google_client.py <google_project_id> <google_client_secret>")
        sys.exit(1)

    project_id = sys.argv[1].strip()
    client_secret = sys.argv[2].strip()
    
    register_client(project_id, client_secret, db_url)
