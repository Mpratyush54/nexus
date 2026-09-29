#!/usr/bin/env python3
"""
deploy/sync-secrets.py
Fetches application secrets directly from AWS Secrets Manager
and safely updates /opt/central-memory/repo/.env on the Lightsail instance.

NO secrets are ever written to git or committed to version control.
"""

import json
import os
import re
import subprocess
import sys

AWS_REGION = os.environ.get("AWS_REGION", "ap-south-1")
LIGHTSAIL_HOST = os.environ.get("LIGHTSAIL_HOST", "13.205.221.207")
LIGHTSAIL_USER = os.environ.get("LIGHTSAIL_USER", "ubuntu")
SSH_KEY = os.environ.get("LIGHTSAIL_KEY", os.path.expanduser("~/.ssh/central-memory-key.pem"))


def get_secret(secret_id):
    cmd = [
        "aws", "secretsmanager", "get-secret-value",
        "--secret-id", secret_id,
        "--region", AWS_REGION,
        "--query", "SecretString",
        "--output", "text"
    ]
    res = subprocess.run(cmd, capture_output=True, text=True)
    if res.returncode != 0:
        print(f"[-] Warning: Failed to fetch {secret_id}: {res.stderr.strip()}", file=sys.stderr)
        return {}
    raw = res.stdout.strip()
    if not raw:
        return {}
    # Try standard JSON
    try:
        return json.loads(raw)
    except Exception:
        pass
    # If wrapped in '{key:val,key2:val2}'
    data = {}
    cleaned = raw.strip("'\"").strip("{}")
    for pair in cleaned.split(","):
        if ":" in pair:
            k, v = pair.split(":", 1)
            data[k.strip()] = v.strip().strip("'\"")
    return data


def run_ssh(command):
    cmd = [
        "ssh", "-i", SSH_KEY,
        "-o", "StrictHostKeyChecking=no",
        f"{LIGHTSAIL_USER}@{LIGHTSAIL_HOST}",
        command
    ]
    return subprocess.run(cmd, capture_output=True, text=True)


def main():
    print(f"[*] Fetching secrets from AWS Secrets Manager ({AWS_REGION})...")

    # 1. JWT
    jwt_data = get_secret("central-memory/jwt")
    jwt_secret = jwt_data.get("signing_key", "")

    # 2. SMTP
    smtp_data = get_secret("central-memory/smtp")
    smtp_host = smtp_data.get("host", "smtp-relay.brevo.com")
    smtp_port = smtp_data.get("port", "587")
    smtp_user = smtp_data.get("username", "")
    smtp_pass = smtp_data.get("password", "")
    mail_from = smtp_data.get("from", "Nexus <noreply@pratyushes.dev>")

    # 3. GitHub
    gh_data = get_secret("central-memory/github")
    gh_client_id = gh_data.get("client_id", "Ov23li997FyAUgaucZQO")
    gh_client_secret = gh_data.get("client_secret", "")

    # 4. OpenRouter
    or_data = get_secret("openrouter")
    openrouter_api_key = or_data.get("api_key", "")

    # Fetch existing DB_PASSWORD from server so we never lose database access
    print("[*] Checking existing DB_PASSWORD on Lightsail...")
    res = run_ssh("grep '^DB_PASSWORD=' /opt/central-memory/repo/.env 2>/dev/null || true")
    existing_db_pw = ""
    if res.returncode == 0 and res.stdout.strip():
        existing_db_pw = res.stdout.strip().split("=", 1)[1]
    if not existing_db_pw:
        import secrets
        existing_db_pw = secrets.token_hex(16)

    env_lines = [
        f"DB_PASSWORD={existing_db_pw}",
        f"JWT_SECRET={jwt_secret}",
        f"GITHUB_CLIENT_ID={gh_client_id}",
        f"GITHUB_CLIENT_SECRET={gh_client_secret}",
        "PUBLIC_API_URL=https://api-nexus.pratyushes.dev",
        "PUBLIC_APP_URL=https://nexus.pratyushes.dev",
        "GITHUB_OAUTH_REDIRECT=https://api-nexus.pratyushes.dev/auth/github/callback",
        f"SMTP_HOST={smtp_host}",
        f"SMTP_PORT={smtp_port}",
        f"SMTP_USER={smtp_user}",
        f"SMTP_PASSWORD={smtp_pass}",
        f"MAIL_FROM={mail_from}",
        f"OPENROUTER_API_KEY={openrouter_api_key}",
        "CENTRAL_MEMORY_LOCAL_DEV=1",
    ]
    env_content = "\n".join(env_lines) + "\n"

    print("[*] Securely writing .env to Lightsail instance...")
    # Write via SSH stdin
    ssh_cmd = [
        "ssh", "-i", SSH_KEY,
        "-o", "StrictHostKeyChecking=no",
        f"{LIGHTSAIL_USER}@{LIGHTSAIL_HOST}",
        "cat > /opt/central-memory/repo/.env && chmod 600 /opt/central-memory/repo/.env"
    ]
    write_res = subprocess.run(ssh_cmd, input=env_content, text=True, capture_output=True)
    if write_res.returncode != 0:
        print(f"[-] Failed to update .env on server: {write_res.stderr}", file=sys.stderr)
        sys.exit(1)

    print("[+] Successfully synced AWS Secrets to Lightsail .env (chmod 600)!")


if __name__ == "__main__":
    main()
