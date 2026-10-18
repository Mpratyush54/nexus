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
    """Return the secret dict, or None when AWS could not be read.

    None means "unknown" (missing permission, expired session, network), not
    "empty": callers must not treat it as "the secret is empty".
    """
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
        return None
    raw = res.stdout.strip()
    if not raw:
        return None
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


def existing_env_value(key):
    """Read one variable from the server .env, or '' when unset."""
    res = run_ssh(f"grep '^{key}=' /opt/central-memory/repo/.env 2>/dev/null || true")
    if res.returncode == 0 and res.stdout.strip():
        return res.stdout.strip().split("=", 1)[1].strip()
    return ""


def resolve_value(label, fetched, env_key, required=True):
    """Never blank a working server secret on a failed AWS fetch.

    Order: AWS value, else the value already on the server. If both are
    empty, refuse to rewrite .env unless ALLOW_MISSING_SECRETS=1 or the
    secret is optional (required=False).
    """
    value = (fetched or "").strip()
    if value:
        return value
    existing = existing_env_value(env_key)
    if existing:
        print(f"[*] Keeping existing {env_key} from server .env ({label} unavailable from AWS)")
        return existing
    if required and os.environ.get("ALLOW_MISSING_SECRETS") != "1":
        print(f"[-] Refusing to write .env: {env_key} is unavailable from AWS Secrets Manager", file=sys.stderr)
        print(f"    and not already set on {LIGHTSAIL_HOST}. Fix the AWS secret/credentials,", file=sys.stderr)
        print("    or re-run with ALLOW_MISSING_SECRETS=1 to write it empty.", file=sys.stderr)
        sys.exit(1)
    print(f"[-] Warning: {env_key} is empty — writing empty value", file=sys.stderr)
    return ""


def main():
    print(f"[*] Fetching secrets from AWS Secrets Manager ({AWS_REGION})...")

    # 1. JWT
    jwt_data = get_secret("central-memory/jwt") or {}
    jwt_secret = resolve_value("central-memory/jwt", jwt_data.get("signing_key", ""), "JWT_SECRET")

    # 2. SMTP
    smtp_data = get_secret("central-memory/smtp") or {}
    smtp_host = smtp_data.get("host", "smtp-relay.brevo.com")
    smtp_port = smtp_data.get("port", "587")
    smtp_user = smtp_data.get("username", "")
    smtp_pass = resolve_value("central-memory/smtp", smtp_data.get("password", ""), "SMTP_PASSWORD", required=False)
    mail_from = smtp_data.get("from", "Nexus <noreply@pratyushes.dev>")

    # 3. GitHub
    gh_data = get_secret("central-memory/github") or {}
    gh_client_id = gh_data.get("client_id", "Ov23li997FyAUgaucZQO")
    gh_client_secret = resolve_value("central-memory/github", gh_data.get("client_secret", ""), "GITHUB_CLIENT_SECRET")

    # 4. OpenRouter — a failed fetch must never blank a working extraction key.
    or_data = get_secret("openrouter") or {}
    openrouter_api_key = resolve_value("openrouter", or_data.get("api_key", ""), "OPENROUTER_API_KEY")

    # Fetch existing DB_PASSWORD from server so we never lose database access
    print("[*] Checking existing DB_PASSWORD on Lightsail...")
    res = run_ssh("grep '^DB_PASSWORD=' /opt/central-memory/repo/.env 2>/dev/null || true")
    existing_db_pw = ""
    if res.returncode == 0 and res.stdout.strip():
        existing_db_pw = res.stdout.strip().split("=", 1)[1]
    if not existing_db_pw:
        import secrets
        existing_db_pw = secrets.token_hex(16)

    # Preserve PLATFORM_ADMIN_USER_IDS across rewrites (bootstrap by user UUID only)
    print("[*] Checking existing PLATFORM_ADMIN_USER_IDS on Lightsail...")
    res = run_ssh("grep '^PLATFORM_ADMIN_USER_IDS=' /opt/central-memory/repo/.env 2>/dev/null || true")
    existing_admin_ids = ""
    if res.returncode == 0 and res.stdout.strip():
        existing_admin_ids = res.stdout.strip().split("=", 1)[1].strip()

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
    if existing_admin_ids:
        env_lines.append(f"PLATFORM_ADMIN_USER_IDS={existing_admin_ids}")
    env_content = "\n".join(env_lines) + "\n"

    print("[*] Securely writing .env to Lightsail instance...")
    # Write via SSH stdin as raw bytes: text mode on Windows would translate
    # "\n" to "\r\n" and leave a stray \r in every value (breaking the API key
    # and JWT). The .env must be LF-only.
    ssh_cmd = [
        "ssh", "-i", SSH_KEY,
        "-o", "StrictHostKeyChecking=no",
        f"{LIGHTSAIL_USER}@{LIGHTSAIL_HOST}",
        "cat > /opt/central-memory/repo/.env && chmod 600 /opt/central-memory/repo/.env"
    ]
    write_res = subprocess.run(ssh_cmd, input=env_content.encode("utf-8"), capture_output=True)
    if write_res.returncode != 0:
        err = write_res.stderr.decode("utf-8", errors="replace")
        print(f"[-] Failed to update .env on server: {err}", file=sys.stderr)
        sys.exit(1)

    print("[+] Successfully synced AWS Secrets to Lightsail .env (chmod 600)!")


if __name__ == "__main__":
    main()
