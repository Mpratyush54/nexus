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


def get_secret(secret_id, known_keys=None):
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
    data = parse_secret_string(raw, known_keys)
    if data is None:
        print(f"[-] {secret_id}: SecretString is neither JSON nor key:value pairs; ignoring it", file=sys.stderr)
    elif not _is_json(raw):
        print(f"[!] {secret_id}: SecretString is not valid JSON (read keys: {', '.join(sorted(data))}).", file=sys.stderr)
        print('    Store it as JSON, e.g. {"host":"...","port":"587","username":"...","password":"...","from":"..."}', file=sys.stderr)
    return data


def _is_json(raw):
    try:
        return isinstance(json.loads(raw), dict)
    except Exception:
        return False


def parse_secret_string(raw, known_keys=None):
    """Parse a SecretString: JSON, or the legacy '{key:val,key2:val2}' form.

    The legacy form may carry a stray quote on one or both ends, and quotes
    around keys or values. Values may contain commas or colons (passwords,
    "Name <a@b>"): a new pair only starts at ",<key>:" where key is an
    identifier. Returns None when nothing usable is found.
    """
    raw = (raw or "").strip()
    if not raw:
        return None
    try:
        data = json.loads(raw)
        if isinstance(data, dict):
            return {str(k): "" if v is None else str(v) for k, v in data.items()}
    except Exception:
        pass
    body = raw.strip().strip("'\"").strip()
    if body.startswith("{"):
        body = body[1:]
    if body.endswith("}"):
        body = body[:-1]
    body = body.strip().strip("'\"").strip()
    key_re = r"(?:^|,)\s*[\"']?([A-Za-z_][A-Za-z0-9_]*)[\"']?\s*:"
    starts = list(re.finditer(key_re, body))
    if known_keys:
        # Only real field names start a pair, so "a,b:c" inside a password stays whole.
        starts = [m for m in starts if m.group(1) in known_keys]
    if not starts:
        return None
    data = {}
    for i, m in enumerate(starts):
        end = starts[i + 1].start() if i + 1 < len(starts) else len(body)
        value = body[m.end():end].strip()
        value = value.strip("'\"").strip()
        data[m.group(1)] = value
    return data


SMTP_KEYS = ("host", "port", "username", "password", "from")


def smtp_settings(data, existing=lambda key: ""):
    """SMTP settings for .env, or exit loudly when they would not work.

    A password without a username is refused: relays like Brevo reject it,
    and every sign-up and password-reset email would fail.
    """
    data = data or {}
    host = (data.get("host") or "").strip() or existing("SMTP_HOST") or "smtp-relay.brevo.com"
    port = (data.get("port") or "").strip() or existing("SMTP_PORT") or "587"
    user = (data.get("username") or "").strip() or existing("SMTP_USER")
    password = (data.get("password") or "").strip() or existing("SMTP_PASSWORD")
    mail_from = (data.get("from") or "").strip() or "Nexus <noreply@pratyushes.dev>"
    problems = []
    if not port.isdigit():
        problems.append(f"port {port!r} is not a number")
    if password and not user:
        problems.append("password is set but username is empty")
    if not password and os.environ.get("ALLOW_MISSING_SECRETS") != "1":
        problems.append("password is empty, so no sign-up or reset email can be sent")
    if problems:
        print("[-] Refusing to write .env: central-memory/smtp is unusable: " + "; ".join(problems), file=sys.stderr)
        print('    Set the secret to JSON: {"host":"smtp-relay.brevo.com","port":"587","username":"<login>",'
              '"password":"<smtp key>","from":"Nexus <noreply@pratyushes.dev>"}', file=sys.stderr)
        print("    (ALLOW_MISSING_SECRETS=1 skips only the empty-password check.)", file=sys.stderr)
        sys.exit(1)
    return host, port, user, password, mail_from


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
    # A failed fetch (None) keeps what the server already has; a secret that
    # parses but is unusable stops the sync instead of deploying broken mail.
    smtp_data = get_secret("central-memory/smtp", SMTP_KEYS)
    if smtp_data is None:
        print("[*] central-memory/smtp unavailable from AWS; keeping the server's SMTP_* values")
        smtp_host, smtp_port, smtp_user, smtp_pass, mail_from = smtp_settings({}, existing_env_value)
    else:
        smtp_host, smtp_port, smtp_user, smtp_pass, mail_from = smtp_settings(smtp_data)
    print(f"[*] mail: smtp host={smtp_host} port={smtp_port} auth={'yes' if smtp_user else 'no'}")

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
    # Preserve operator overrides of the embeddings wiring
    # (e.g. CENTRAL_EMBEDDING_PROVIDER=hash as a kill switch). Defaults live
    # in deploy/docker-compose.lightsail.yml.
    res = run_ssh("grep '^CENTRAL_EMBEDDING_[A-Z_]*=' /opt/central-memory/repo/.env 2>/dev/null || true")
    if res.returncode == 0:
        for line in res.stdout.splitlines():
            line = line.strip()
            if re.match(r"^CENTRAL_EMBEDDING_[A-Z_]+=", line):
                env_lines.append(line)
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
