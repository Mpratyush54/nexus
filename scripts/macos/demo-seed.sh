#!/usr/bin/env bash
# Fills a demo central server (scripts/macos/demo-server.sh) with FICTIONAL
# data through the real HTTP API, and optionally signs a desktop profile in.
#
#   scripts/macos/demo-seed.sh [PROFILE_ROOT]
#
# Creates: the user "Ada Demo" <ada@example.test> (email signup with the
# local-dev code), a long-lived API token, the project "Orbit Notes", five
# chats from Claude Code, Codex and Cursor (user/assistant turns, tool calls,
# token usage), project memories tied to those chats, the files each chat
# touched (for the graph) and, when the server can, a project brief.
#
# With PROFILE_ROOT (the NEXUS_PROFILE_ROOT the app runs with):
#   $ROOT/Library/Application Support/central-memory/config.json
#       server_url, token, user_id, username: the Go core's sign-in. On macOS
#       the core reads os.UserConfigDir()/central-memory = $HOME/Library/
#       Application Support/central-memory, and a profile's HOME is $ROOT.
#   $ROOT/AppData/Local/Nexus/ui-prefs.json
#       setup done, first-run screens seen, the project selected, Home first.
#   $ROOT/.claude/projects/<slug>/<native_id>.jsonl
#       the local Claude Code transcript of the first chat, holding one
#       screenshot (chats show images only from the transcript on this Mac).
#   $ROOT/profile.env
#       exports to launch the app with (HOME, CENTRAL_MEMORY_CONFIG_DIR, ...):
#       .NET's Environment.SetEnvironmentVariable does not change the native
#       environment on macOS, so the in-process Go core may not see the HOME
#       that ProfilePaths applies. Sourcing this before launching makes both
#       agree.
#
# Prints and (on GitHub Actions) exports NEXUS_DEMO_TOKEN, NEXUS_DEMO_PROJECT_ID,
# NEXUS_DEMO_USER_ID, NEXUS_DEMO_SESSION_ID (the chat with the image) and
# NEXUS_DEMO_SERVER.
#
# Environment (optional): NEXUS_DEMO_SERVER (default http://127.0.0.1:8787),
# NEXUS_DEMO_EMAIL, NEXUS_DEMO_USERNAME, NEXUS_DEMO_PASSWORD,
# NEXUS_DEMO_THEME (dark), NEXUS_DEMO_LAST_NAV (home).
# Needs curl and jq; python3 draws the screenshot (a 1x1 image without it).
# Re-running against the same server is safe: it signs in instead of signing
# up and the API upserts chats, turns and memories.
set -euo pipefail

ROOT=${1:-}
SERVER=${NEXUS_DEMO_SERVER:-http://127.0.0.1:8787}
SERVER=${SERVER%/}
EMAIL=${NEXUS_DEMO_EMAIL:-ada@example.test}
USERNAME=${NEXUS_DEMO_USERNAME:-Ada Demo}
PASSWORD=${NEXUS_DEMO_PASSWORD:-orbit-demo-password-1}
THEME=${NEXUS_DEMO_THEME:-dark}
LAST_NAV=${NEXUS_DEMO_LAST_NAV:-home}
PROJECT_NAME="Orbit Notes"
PROJECT_URL="https://github.com/example-org/orbit-notes.git"
WORKSPACE_HINT="/Users/ada/code/orbit-notes"
MACHINE_ID="demo-macbook-ada"
NOW=$(date +%s)

command -v jq > /dev/null || { echo "::error::demo-seed: jq is required" >&2; exit 1; }
command -v curl > /dev/null || { echo "::error::demo-seed: curl is required" >&2; exit 1; }

if [ -n "$ROOT" ]; then
  mkdir -p "$ROOT"
  ROOT=$(cd "$ROOT" && pwd)
fi

WORK=$(mktemp -d "${TMPDIR:-/tmp}/nexus-seed.XXXXXX")
trap 'rm -rf "$WORK"' EXIT

log() { echo "[demo-seed $(date +%T)] $*"; }
die() { echo "::error::demo-seed: $*" >&2; exit 1; }

TOKEN=""
# api METHOD PATH [BODY_JSON] [EXTRA_OK_STATUS]
# Prints the response body. Any status outside 2xx (and EXTRA_OK_STATUS) is
# fatal and prints the server's error body. (Not curl -f: that hides the body.)
api() {
  local method=$1 path=$2 body=${3-} extra_ok=${4-}
  local out="$WORK/response" status
  local args=(-sS -m 60 -o "$out" -w '%{http_code}' -X "$method" "$SERVER$path")
  if [ -n "$TOKEN" ]; then args+=(-H "Authorization: Bearer $TOKEN"); fi
  if [ -n "$body" ]; then
    args+=(-H "Content-Type: application/json" --data-binary @-)
    status=$(printf '%s' "$body" | curl "${args[@]}") || die "$method $path: curl failed"
  else
    status=$(curl "${args[@]}") || die "$method $path: curl failed"
  fi
  # api usually runs in $(...): keep the status where the caller can read it.
  printf '%s' "$status" > "$WORK/status"
  if [[ "$status" == 2?? ]] || { [ -n "$extra_ok" ] && [ "$status" = "$extra_ok" ]; }; then
    cat "$out"
    return 0
  fi
  echo "::error::demo-seed: $method $path -> HTTP $status" >&2
  echo "request body: $(printf '%s' "$body" | head -c 2000)" >&2
  echo "response body: $(head -c 4000 "$out")" >&2
  exit 1
}
# jget JSON FILTER: a required field.
jget() {
  local v
  v=$(printf '%s' "$1" | jq -er "$2") || die "missing $2 in: $(printf '%s' "$1" | head -c 1000)"
  printf '%s' "$v"
}

# ---- 0. Server up -----------------------------------------------------------
for _ in $(seq 1 60); do
  curl -fsS -m 2 "$SERVER/healthz" > /dev/null 2>&1 && break
  sleep 1
done
curl -fsS -m 5 "$SERVER/healthz" > /dev/null || die "$SERVER/healthz is not answering (run demo-server.sh first)"

# ---- 1. Account: signup with the local-dev code, else sign in ---------------
log "signing up $EMAIL"
SIGNUP=$(jq -nc --arg u "$USERNAME" --arg p "$PASSWORD" --arg e "$EMAIL" \
  '{username: $u, password: $p, email: $e}')
SENT=$(api POST /auth/signup/send-code "$SIGNUP" 409)
if [ "$(cat "$WORK/status")" = 409 ]; then
  log "account exists; signing in"
  LOGIN=$(api POST /auth/login "$(jq -nc --arg u "$EMAIL" --arg p "$PASSWORD" '{username: $u, password: $p}')")
else
  CODE=$(printf '%s' "$SENT" | jq -r '.dev_code // empty')
  [ -n "$CODE" ] || die "send-code returned no dev_code (is CENTRAL_MEMORY_LOCAL_DEV=1 set and mail unconfigured?): $SENT"
  LOGIN=$(api POST /auth/signup "$(jq -nc --arg u "$USERNAME" --arg p "$PASSWORD" --arg e "$EMAIL" --arg c "$CODE" \
    '{username: $u, password: $p, email: $e, code: $c}')")
fi
TOKEN=$(jget "$LOGIN" .token)
USER_ID=$(jget "$LOGIN" .user_id)

# The desktop signs in with a long-lived API token (core auth.connect saves
# one into config.json); the 24 h session JWT is only used to mint it.
TOK=$(api POST /auth/tokens "$(jq -nc '{name: "Nexus for Mac (demo)", scopes: []}')")
TOKEN=$(jget "$TOK" .token)
case "$TOKEN" in nxs_*) ;; *) die "unexpected API token shape" ;; esac
ME=$(api GET /users/me)
[ "$(jget "$ME" .id)" = "$USER_ID" ] || die "/users/me disagrees about the user id"
USER_NAME=$(jget "$ME" .username)
log "user $USER_NAME ($USER_ID)"

# ---- 2. Project -------------------------------------------------------------
# POST /projects/resolve creates the project on first sight and makes the
# first resolver its creator (owner); GET /v1/agent/projects lists it.
PROJECT=$(api POST /projects/resolve "$(jq -nc --arg url "$PROJECT_URL" --arg name "$PROJECT_NAME" \
  '{canonical_url: $url, folder_name: $name}')")
PROJECT_ID=$(jget "$PROJECT" .id)
[ "$(printf '%s' "$PROJECT" | jq -r .created_by)" = "$USER_ID" ] \
  || die "project $PROJECT_ID belongs to someone else (created_by=$(printf '%s' "$PROJECT" | jq -r .created_by))"
log "project $PROJECT_NAME ($PROJECT_ID)"

# ---- 3. Chats -----------------------------------------------------------------
# ago = minutes before now. Tool calls use the shape capture uploads
# (name, id, input/args) plus a result for the chat's work log.
cat > "$WORK/chats.json" << 'JSON'
[
  {
    "key": "sync", "harness": "claude", "native_id": "5d2f8c1e-3a47-4b9e-8f10-2c6a7e9b4d31",
    "title": "Fix sync conflict when two devices edit the same note",
    "summary": "Replaced last-write-wins with a per-paragraph three-way merge and a conflict banner for overlapping edits.",
    "model": "claude-sonnet-4-5", "image_turn": 0,
    "files": ["src/sync/merge.ts", "src/sync/merge.test.ts", "src/ui/ConflictBanner.tsx"],
    "turns": [
      {"role": "user", "ago": 2885, "text": "Two devices edited the same note offline and after sync one person's paragraph vanished. Screenshot of what the second device showed is attached. Can you make sync keep both edits?"},
      {"role": "assistant", "ago": 2884, "text": "Let me look at how sync merges note bodies today.", "tool_calls": [
        {"id": "toolu_01", "name": "Read", "input": {"file_path": "src/sync/merge.ts"}, "result": "export function mergeNote(local, remote) {\n  return local.updatedAt > remote.updatedAt ? local : remote;\n}"}]},
      {"role": "assistant", "ago": 2883, "text": "Sync is last-write-wins on the whole note, so the older device loses everything it typed. I'll switch to a three-way merge per paragraph against the last synced revision, and only flag paragraphs both devices changed.", "usage": {"model": "claude-sonnet-4-5", "input": 18420, "output": 612, "cache_read": 12000}},
      {"role": "assistant", "ago": 2880, "text": "", "tool_calls": [
        {"id": "toolu_02", "name": "Edit", "input": {"file_path": "src/sync/merge.ts", "old_string": "return local.updatedAt > remote.updatedAt ? local : remote;", "new_string": "return mergeParagraphs(base, local, remote);"}, "result": "The file src/sync/merge.ts has been updated."}]},
      {"role": "assistant", "ago": 2876, "text": "", "tool_calls": [
        {"id": "toolu_03", "name": "Bash", "input": {"command": "npm test -- src/sync", "description": "Run the sync tests"}, "result": "PASS src/sync/merge.test.ts\n  mergeParagraphs\n    keeps edits to different paragraphs\n    flags a paragraph both devices changed\n\nTests: 2 passed, 2 total"}]},
      {"role": "assistant", "ago": 2875, "text": "Done. Edits to different paragraphs now merge silently; when both devices change the same paragraph the note keeps both versions and shows the conflict banner so the person can pick one. Added tests for both cases.", "usage": {"model": "claude-sonnet-4-5", "input": 21877, "output": 940, "cache_read": 18000}},
      {"role": "user", "ago": 2860, "text": "Nice. Can the banner say which device made each version?"},
      {"role": "assistant", "ago": 2858, "text": "Yes: the merge result now carries the device name from each revision, and the banner reads \"Edited on Ada's iPad\" / \"Edited on this Mac\".", "tool_calls": [
        {"id": "toolu_04", "name": "Edit", "input": {"file_path": "src/ui/ConflictBanner.tsx", "old_string": "<p>Two versions</p>", "new_string": "<p>Edited on {left.device} / {right.device}</p>"}, "result": "The file src/ui/ConflictBanner.tsx has been updated."}],
       "usage": {"model": "claude-sonnet-4-5", "input": 23410, "output": 388, "cache_read": 21000}}
    ]
  },
  {
    "key": "export", "harness": "codex", "native_id": "0193a7c2-6e1f-7b40-9c55-3f8d2a61e0b7",
    "title": "Add Markdown export for notebooks",
    "summary": "Notebook export writes one Markdown file per note with front matter and relative image links.",
    "model": "gpt-5-codex",
    "files": ["src/export/markdown.ts", "src/export/markdown.test.ts"],
    "turns": [
      {"role": "user", "ago": 1450, "text": "Add an Export to Markdown action for a whole notebook. One .md file per note, keep tags and created date."},
      {"role": "assistant", "ago": 1449, "text": "", "tool_calls": [
        {"id": "call_a1", "name": "exec_command", "input": {"cmd": "rg -n \"exportNotebook\" src"}, "result": "src/export/index.ts:12:export async function exportNotebook(id, format) {"}]},
      {"role": "assistant", "ago": 1447, "text": "", "tool_calls": [
        {"id": "call_a2", "name": "apply_patch", "input": {"patch": "*** Begin Patch\n*** Add File: src/export/markdown.ts\n+export function noteToMarkdown(note) {\n+  const front = ['---', `title: ${note.title}`, `tags: [${note.tags.join(', ')}]`, `created: ${note.createdAt}`, '---'];\n+  return [...front, '', note.body].join('\\n');\n+}\n*** End Patch"}, "result": "Success. Updated the following files:\nA src/export/markdown.ts"}]},
      {"role": "assistant", "ago": 1444, "text": "Export is in: each note becomes `<slug>.md` with YAML front matter (title, tags, created) and images are copied next to it with relative links. `npm test -- export` passes.", "usage": {"model": "gpt-5-codex", "input": 9310, "output": 1204, "cache_read": 4096}},
      {"role": "user", "ago": 1440, "text": "Slugs collide when two notes share a title."},
      {"role": "assistant", "ago": 1437, "text": "Fixed: a colliding slug gets a short suffix from the note id (`groceries-3f2a.md`), and the test covers two notes titled the same.", "tool_calls": [
        {"id": "call_a3", "name": "exec_command", "input": {"cmd": "npm test -- src/export"}, "result": "PASS src/export/markdown.test.ts (4 tests)"}],
       "usage": {"model": "gpt-5-codex", "input": 11020, "output": 506, "cache_read": 8192}}
    ]
  },
  {
    "key": "search", "harness": "cursor", "native_id": "c41b9e07-2d58-4f6a-b3e1-8a7d0f2c95e4",
    "title": "Speed up full-text search on large libraries",
    "summary": "Moved search to SQLite FTS5 with a trigram tokenizer; 40k-note library queries dropped from 900 ms to 35 ms.",
    "model": "cursor-fast",
    "files": ["src/search/index.ts", "src/db/migrations/014_fts5.sql"],
    "turns": [
      {"role": "user", "ago": 600, "text": "Search takes almost a second on my 40k-note test library. Can we make it instant?"},
      {"role": "assistant", "ago": 598, "text": "", "tool_calls": [
        {"id": "tc_1", "name": "read_file", "input": {"target_file": "src/search/index.ts"}, "result": "notes.filter(n => n.body.toLowerCase().includes(q))"}]},
      {"role": "assistant", "ago": 596, "text": "Search scans every note body in JavaScript. I'll add an FTS5 table with the trigram tokenizer (so substring matches still work) and keep it in sync with triggers.", "tool_calls": [
        {"id": "tc_2", "name": "edit_file", "input": {"target_file": "src/db/migrations/014_fts5.sql", "code_edit": "CREATE VIRTUAL TABLE notes_fts USING fts5(title, body, content='notes', tokenize='trigram');"}, "result": "Applied"}]},
      {"role": "assistant", "ago": 590, "text": "", "tool_calls": [
        {"id": "tc_3", "name": "run_terminal_cmd", "input": {"command": "npm run bench:search"}, "result": "before: p50 912 ms\nafter:  p50 35 ms"}]},
      {"role": "assistant", "ago": 589, "text": "Median query time went from 912 ms to 35 ms on the 40k-note library. The index adds about 18% to the database size."},
      {"role": "user", "ago": 585, "text": "Good trade. Ship it."}
    ]
  },
  {
    "key": "release", "harness": "claude", "native_id": "9a6e4d12-7c3b-4e85-a0f9-1b2c3d4e5f60",
    "title": "Draft release notes for Orbit Notes 2.4",
    "summary": "Release notes for 2.4: conflict-safe sync, Markdown export, faster search.",
    "model": "claude-sonnet-4-5",
    "files": ["CHANGELOG.md"],
    "turns": [
      {"role": "user", "ago": 180, "text": "Write the 2.4 release notes from this week's work. Short, user-facing, no internal names."},
      {"role": "assistant", "ago": 179, "text": "", "tool_calls": [
        {"id": "toolu_11", "name": "Bash", "input": {"command": "git log --oneline v2.3.0..HEAD", "description": "List changes since 2.3"}, "result": "a1c9f02 Search: FTS5 trigram index\n7be3310 Export notebooks to Markdown\n52d0a8e Sync: merge paragraphs, conflict banner"}]},
      {"role": "assistant", "ago": 178, "text": "## Orbit Notes 2.4\n\n- **Edits never get lost.** If you change the same note on two devices, Orbit keeps both edits and shows you exactly where they overlap.\n- **Export to Markdown.** Export a whole notebook as Markdown files, tags and dates included.\n- **Instant search.** Search is up to 25x faster on large libraries.", "tool_calls": [
        {"id": "toolu_12", "name": "Edit", "input": {"file_path": "CHANGELOG.md", "old_string": "# Changelog\n", "new_string": "# Changelog\n\n## 2.4.0\n"}, "result": "The file CHANGELOG.md has been updated."}],
       "usage": {"model": "claude-sonnet-4-5", "input": 6120, "output": 410}}
    ]
  },
  {
    "key": "flaky", "harness": "codex", "native_id": "0193b8d4-1a2b-7c3d-8e4f-5a6b7c8d9e0f",
    "title": "Flaky reminder test fails around midnight UTC",
    "summary": "The reminders scheduler test used the wall clock; it now runs on an injected clock.",
    "model": "gpt-5-codex",
    "files": ["src/reminders/scheduler.ts", "src/reminders/scheduler.test.ts"],
    "turns": [
      {"role": "user", "ago": 35, "text": "scheduler.test.ts fails on CI maybe once a week. Always close to midnight UTC."},
      {"role": "assistant", "ago": 33, "text": "", "tool_calls": [
        {"id": "call_b1", "name": "exec_command", "input": {"cmd": "rg -n \"new Date\\(\\)\" src/reminders"}, "result": "src/reminders/scheduler.ts:41:  const today = new Date();"}]},
      {"role": "assistant", "ago": 31, "text": "The scheduler reads the wall clock directly, so a test that starts at 23:59:59 sees the date roll over halfway through. I'm passing a clock into the scheduler and freezing it in the test.", "tool_calls": [
        {"id": "call_b2", "name": "apply_patch", "input": {"patch": "*** Begin Patch\n*** Update File: src/reminders/scheduler.ts\n-export function nextRun(rule) {\n-  const today = new Date();\n+export function nextRun(rule, clock = systemClock) {\n+  const today = clock.now();\n*** End Patch"}, "result": "Success. Updated the following files:\nM src/reminders/scheduler.ts"}],
       "usage": {"model": "gpt-5-codex", "input": 7302, "output": 655}},
      {"role": "assistant", "ago": 28, "text": "Ran the test 500 times with the clock pinned to 23:59:59.900 UTC: all green."}
    ]
  }
]
JSON

# Memories: scope, chat they came from (tag session:<id>, which the graph
# draws as an edge), files they are about.
cat > "$WORK/memories.json" << 'JSON'
[
  {"chat": "sync", "key": "sync.merge-strategy", "scope": "decision", "category": "architecture", "confidence": 0.95, "confirm": true, "pin": true,
   "tags": ["sync"], "files_affected": ["src/sync/merge.ts"],
   "content": "Note sync merges concurrent edits per paragraph with a three-way merge against the last synced revision. Last-write-wins on whole notes was removed because it silently dropped the older device's edits."},
  {"chat": "sync", "key": "sync.conflict-banner", "scope": "pattern", "category": "conventions", "confidence": 0.85, "confirm": true,
   "tags": ["sync", "ui"], "files_affected": ["src/ui/ConflictBanner.tsx"],
   "content": "When both devices change the same paragraph, keep both versions and show ConflictBanner with the device name of each revision; never pick a winner automatically."},
  {"chat": "export", "key": "export.markdown-format", "scope": "decision", "category": "api", "confidence": 0.9, "confirm": true,
   "tags": ["export"], "files_affected": ["src/export/markdown.ts"],
   "content": "Markdown export writes one <slug>.md per note with YAML front matter (title, tags, created) and copies images next to it with relative links."},
  {"chat": "export", "key": "export.slug-collisions", "scope": "constraint", "category": "conventions", "confidence": 0.8,
   "tags": ["export"], "files_affected": ["src/export/markdown.ts", "src/export/markdown.test.ts"],
   "content": "Export file names must be unique within a notebook: a colliding slug gets a four-character suffix from the note id, for example groceries-3f2a.md."},
  {"chat": "search", "key": "search.fts5-trigram", "scope": "decision", "category": "architecture", "confidence": 0.9, "confirm": true,
   "tags": ["search", "performance"], "files_affected": ["src/search/index.ts", "src/db/migrations/014_fts5.sql"],
   "content": "Full-text search uses an SQLite FTS5 table with the trigram tokenizer, kept in sync by triggers, so substring queries stay fast: p50 went from 912 ms to 35 ms on a 40k-note library."},
  {"chat": "search", "key": "search.index-size-budget", "scope": "fact", "category": "infrastructure", "confidence": 0.7,
   "tags": ["search"], "files_affected": ["src/db/migrations/014_fts5.sql"],
   "content": "The FTS5 trigram index adds about 18% to the database size; that trade was accepted for instant search."},
  {"chat": "release", "key": "docs.release-notes-voice", "scope": "preference", "category": "conventions", "confidence": 0.85, "confirm": true,
   "tags": ["docs"], "files_affected": ["CHANGELOG.md"],
   "content": "Release notes are short and user-facing: a bold one-line benefit per item, no internal module names or ticket numbers."},
  {"chat": "flaky", "key": "testing.injected-clock", "scope": "pattern", "category": "conventions", "confidence": 0.9, "confirm": true,
   "tags": ["testing", "reminders"], "files_affected": ["src/reminders/scheduler.ts", "src/reminders/scheduler.test.ts"],
   "content": "Code that depends on the date takes a clock parameter (default systemClock); tests freeze it instead of reading the wall clock, which made the reminder tests flaky around midnight UTC."}
]
JSON

# A screenshot for the first chat: a mock "two versions" sync dialog.
SHOT_B64="$WORK/shot.b64"
if command -v python3 > /dev/null 2>&1; then
  python3 - "$SHOT_B64" << 'PY'
import base64, struct, sys, zlib
W, H = 480, 270
px = [[(243, 244, 246)] * W for _ in range(H)]
def rect(x0, y0, x1, y1, c):
    for y in range(y0, y1):
        row = px[y]
        for x in range(x0, x1):
            row[x] = c
rect(40, 36, 440, 234, (255, 255, 255))      # dialog
rect(40, 36, 440, 72, (234, 88, 12))         # warning header
rect(60, 50, 220, 58, (255, 237, 213))       # header text
rect(60, 92, 236, 104, (31, 41, 55))         # "this Mac" label
rect(60, 112, 230, 120, (156, 163, 175))
rect(60, 126, 210, 134, (156, 163, 175))
rect(60, 140, 224, 148, (156, 163, 175))
rect(248, 92, 420, 104, (31, 41, 55))        # "iPad" label
rect(248, 112, 418, 120, (156, 163, 175))
rect(248, 126, 380, 134, (156, 163, 175))
rect(248, 140, 400, 148, (156, 163, 175))
rect(240, 88, 242, 160, (229, 231, 235))     # divider
rect(250, 186, 330, 214, (37, 99, 235))      # keep both
rect(340, 186, 420, 214, (229, 231, 235))    # cancel
raw = b"".join(b"\x00" + bytes(v for p in row for v in p) for row in px)
def chunk(t, d):
    c = struct.pack(">I", len(d)) + t + d
    return c + struct.pack(">I", zlib.crc32(t + d) & 0xFFFFFFFF)
png = b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", W, H, 8, 2, 0, 0, 0)) \
    + chunk(b"IDAT", zlib.compress(raw, 9)) + chunk(b"IEND", b"")
open(sys.argv[1], "w").write(base64.b64encode(png).decode())
PY
else
  printf '%s' 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==' > "$SHOT_B64"
fi

CHAT_COUNT=$(jq length "$WORK/chats.json")
TURN_TOTAL=0
SESSIONS='{}'
IMAGE_SESSION_ID=""
for ((i = 0; i < CHAT_COUNT; i++)); do
  jq ".[$i]" "$WORK/chats.json" > "$WORK/chat.json"
  KEY=$(jq -r .key "$WORK/chat.json")
  HARNESS=$(jq -r .harness "$WORK/chat.json")
  NATIVE=$(jq -r .native_id "$WORK/chat.json")
  N=$(jq '.turns | length' "$WORK/chat.json")

  SESSION=$(api POST /v1/agent-sessions "$(jq -c --arg p "$PROJECT_ID" --arg m "$MACHINE_ID" --arg w "$WORKSPACE_HINT" \
    '{project_id: $p, harness: .harness, native_id: .native_id, origin_machine_id: $m,
      title: .title, workspace_root_hint: $w, summary: .summary}' "$WORK/chat.json")")
  SID=$(jget "$SESSION" .id)
  SESSIONS=$(jq -c --arg k "$KEY" --arg id "$SID" '. + {($k): $id}' <<< "$SESSIONS")

  # Turn indexes. Claude Code capture numbers a turn by its line's position
  # in the local transcript, which is how the chat places transcript images:
  # for the chat with a screenshot, write that transcript and use its
  # positions. Other chats count from 1.
  IDX=$(jq -c '[range(1; (.turns | length) + 1)]' "$WORK/chat.json")
  if [ -n "$ROOT" ] && jq -e 'has("image_turn")' "$WORK/chat.json" > /dev/null; then
    SLUG=$(printf '%s' "$WORKSPACE_HINT" | tr '/.' '--')
    TRANSCRIPT="$ROOT/.claude/projects/$SLUG/$NATIVE.jsonl"
    mkdir -p "$(dirname "$TRANSCRIPT")"
    jq -c --rawfile img "$SHOT_B64" --argjson now "$NOW" --arg cwd "$WORKSPACE_HINT" '
      . as $c | .turns | to_entries[] | .key as $n | .value as $t |
      {type: $t.role, sessionId: $c.native_id, cwd: $cwd, uuid: ("demo-\($c.key)-\($n)"),
       timestamp: (($now - $t.ago * 60) | todate),
       message: ({role: $t.role,
                  content: ((if ($t.text // "") != "" then [{type: "text", text: $t.text}] else [] end)
                    + (if $n == $c.image_turn then [{type: "image", source: {type: "base64", media_type: "image/png", data: ($img | rtrimstr("\n"))}}] else [] end)
                    + [($t.tool_calls // [])[] | {type: "tool_use", id: .id, name: .name, input: .input}])}
                 + (if $t.role == "assistant" then {model: $c.model} else {} end))}' \
      "$WORK/chat.json" > "$TRANSCRIPT"
    # Position = every earlier line's length without its newline, plus one per line.
    IDX=$(LC_ALL=C awk '{ pos += length($0) + 1; print pos }' "$TRANSCRIPT" | jq -s -c .)
    [ "$(jq length <<< "$IDX")" = "$N" ] || die "transcript line count does not match the turns of $KEY"
    IMAGE_SESSION_ID=$SID
    log "wrote $TRANSCRIPT"
  fi

  TURNS=$(jq -c --argjson idx "$IDX" --argjson now "$NOW" '
    {turns: [.turns | to_entries[] | .key as $n | .value as $t |
      {idx: $idx[$n], role: $t.role, text_preview: ($t.text // ""), tool_calls: ($t.tool_calls // []),
       source_ts: (($now - $t.ago * 60) | todate)}
      + (if $t.usage then {usage: $t.usage} else {} end)]}' "$WORK/chat.json")
  RES=$(api POST "/v1/agent-sessions/$SID/turns/batch" "$TURNS")
  log "chat [$HARNESS] $(jq -r .title "$WORK/chat.json"): $SID ($(jq -r '"\(.created) new, \(.duplicates) already there"' <<< "$RES"))"
  TURN_TOTAL=$((TURN_TOTAL + N))
done

# ---- 4. Memories ------------------------------------------------------------
MEM_COUNT=$(jq length "$WORK/memories.json")
for ((i = 0; i < MEM_COUNT; i++)); do
  M=$(jq -c ".[$i]" "$WORK/memories.json")
  SID=$(jq -r --argjson s "$SESSIONS" '$s[.chat]' <<< "$M")
  BODY=$(jq -c --arg p "$PROJECT_ID" --arg sid "$SID" '
    {project_id: $p, key: .key, content: .content, level: "project", scope: .scope,
     category: .category, confidence: .confidence, files_affected: .files_affected,
     tags: (.tags + ["session:" + $sid]), source: "demo-seed"}' <<< "$M")
  ITEM=$(api POST /memory "$BODY")
  MID=$(jget "$ITEM" .id)
  if jq -e '.confirm == true' <<< "$M" > /dev/null; then api POST "/memory/$MID/confirm" '{}' > /dev/null; fi
  if jq -e '.pin == true' <<< "$M" > /dev/null; then api POST "/memory/$MID/pin" '{}' > /dev/null; fi
  log "memory $(jq -r .key <<< "$M"): $MID"
done

# ---- 5. Files each chat touched (graph file nodes and memory links) --------
for ((i = 0; i < CHAT_COUNT; i++)); do
  KEY=$(jq -r ".[$i].key" "$WORK/chats.json")
  SID=$(jq -r --arg k "$KEY" '.[$k]' <<< "$SESSIONS")
  BODY=$(jq -c --arg sid "$SID" ".[$i] | {session_id: \$sid, harness: .harness, links: [.files[] | {path: .}]}" "$WORK/chats.json")
  RES=$(api POST "/v1/projects/$PROJECT_ID/code-links" "$BODY")
  log "files for $KEY: $(jq -r '"\(.linked) linked, \(.memory_links) memory links"' <<< "$RES")"
done

# ---- 6. Project brief (best effort: it may need an LLM key) ---------------
BRIEF_STATUS=$(curl -sS -m 100 -o "$WORK/brief" -w '%{http_code}' -X POST \
  -H "Authorization: Bearer $TOKEN" "$SERVER/v1/projects/$PROJECT_ID/brief/refresh" || echo 000)
case "$BRIEF_STATUS" in
  2??) log "project brief built" ;;
  *) log "project brief not built (HTTP $BRIEF_STATUS: $(head -c 300 "$WORK/brief" 2> /dev/null)); Memory shows without it" ;;
esac

# ---- 7. Check what the desktop will read -----------------------------------
PROJECTS=$(api GET /v1/agent/projects)
jq -e --arg id "$PROJECT_ID" '.items | any(.id == $id)' <<< "$PROJECTS" > /dev/null || die "the project is missing from /v1/agent/projects"
TIMELINE=$(api GET "/v1/timeline?project_id=$PROJECT_ID&limit=50")
CHATS_SEEN=$(jq '.items | length' <<< "$TIMELINE")
[ "$CHATS_SEEN" -ge "$CHAT_COUNT" ] || die "timeline shows $CHATS_SEEN chats, expected $CHAT_COUNT: $TIMELINE"
KNOWLEDGE=$(api GET "/v1/projects/$PROJECT_ID/knowledge?limit=50")
MEMS_SEEN=$(jq '.items | length' <<< "$KNOWLEDGE")
[ "$MEMS_SEEN" -ge "$MEM_COUNT" ] || die "knowledge shows $MEMS_SEEN memories, expected $MEM_COUNT"
GRAPH=$(api GET "/v1/projects/$PROJECT_ID/graph?limit=300")
GRAPH_SUMMARY=$(jq -r '"\(.nodes | map(select(.kind == "memory")) | length) memories, \(.nodes | map(select(.kind == "session")) | length) chats, \(.nodes | map(select(.kind == "file")) | length) files, \(.edges | length) edges"' <<< "$GRAPH")
[ "$(jq '.edges | length' <<< "$GRAPH")" -gt 0 ] || die "the graph has no edges: $GRAPH"
DASH=$(api GET "/projects/$PROJECT_ID/dashboard?utc_offset_minutes=0")
TURNS_SEEN=$(jq '[.items[].turn_count // 0] | add // 0' <<< "$TIMELINE")
log "check: $CHATS_SEEN chats ($TURNS_SEEN turns, $TURN_TOTAL sent), $MEMS_SEEN memories, graph $GRAPH_SUMMARY, dashboard memories=$(jq -r '.metrics.memories' <<< "$DASH")"

# ---- 8. Desktop profile -----------------------------------------------------
if [ -n "$ROOT" ]; then
  CONFIG_DIR="$ROOT/Library/Application Support/central-memory"
  PREFS_DIR="$ROOT/AppData/Local/Nexus"
  mkdir -p "$CONFIG_DIR" "$PREFS_DIR" "$ROOT/AppData/Roaming" "$ROOT/AppData/Local/Temp" "$ROOT/Documents"
  chmod 700 "$CONFIG_DIR"
  CONFIG="$CONFIG_DIR/config.json"
  OLD_CONFIG='{}'
  if [ -s "$CONFIG" ]; then OLD_CONFIG=$(cat "$CONFIG"); fi
  # Merge over whatever is there (device ids, capture choices); the sign-in
  # fields are replaced. token_protected is Windows-only and would fail to load.
  jq --arg s "$SERVER" --arg t "$TOKEN" --arg u "$USER_ID" --arg n "$USER_NAME" \
    'del(.token_protected) + {server_url: $s, token: $t, user_id: $u, username: $n}' <<< "$OLD_CONFIG" > "$CONFIG.tmp"
  chmod 600 "$CONFIG.tmp"
  mv "$CONFIG.tmp" "$CONFIG"
  log "wrote $CONFIG"

  PREFS="$PREFS_DIR/ui-prefs.json"
  OLD_PREFS='{}'
  if [ -s "$PREFS" ]; then OLD_PREFS=$(cat "$PREFS"); fi
  # Keys are AppPrefs.PrefsData property names (System.Text.Json, case-sensitive).
  jq --arg id "$PROJECT_ID" --arg name "$PROJECT_NAME" --arg nav "$LAST_NAV" --arg theme "$THEME" \
    '. + {SetupCompleted: true, MacWelcomeSeen: true, MacMoveDeclined: true,
          ProjectId: $id, ProjectName: $name, LastNav: $nav, Theme: $theme,
          MemoryScope: "all", LastStartAgent: "claude"}' <<< "$OLD_PREFS" > "$PREFS.tmp"
  mv "$PREFS.tmp" "$PREFS"
  log "wrote $PREFS"

  # What ProfilePaths.EnvironmentFor applies, for the real process
  # environment of the app (and so of the in-process Go core).
  {
    printf 'export HOME=%q\n' "$ROOT"
    printf 'export USERPROFILE=%q\n' "$ROOT"
    printf 'export APPDATA=%q\n' "$ROOT/AppData/Roaming"
    printf 'export LOCALAPPDATA=%q\n' "$ROOT/AppData/Local"
    printf 'export CENTRAL_MEMORY_CONFIG_DIR=%q\n' "$CONFIG_DIR"
    printf 'unset NEXUS_SERVER CENTRAL_SERVER_URL NEXUS_TOKEN CENTRAL_MEMORY_TOKEN NEXUS_CONFIG_DIR CODEX_HOME CLAUDE_CONFIG_DIR XDG_CONFIG_HOME XDG_DATA_HOME XDG_CACHE_HOME\n'
  } > "$ROOT/profile.env"
  log "wrote $ROOT/profile.env"
fi

# ---- 9. Outputs ---------------------------------------------------------------
if [ -n "${GITHUB_ENV:-}" ]; then
  echo "::add-mask::$TOKEN"
  {
    echo "NEXUS_DEMO_SERVER=$SERVER"
    echo "NEXUS_DEMO_TOKEN=$TOKEN"
    echo "NEXUS_DEMO_PROJECT_ID=$PROJECT_ID"
    echo "NEXUS_DEMO_USER_ID=$USER_ID"
    echo "NEXUS_DEMO_SESSION_ID=${IMAGE_SESSION_ID:-$(jq -r .sync <<< "$SESSIONS")}"
  } >> "$GITHUB_ENV"
fi
echo "NEXUS_DEMO_SERVER=$SERVER"
echo "NEXUS_DEMO_TOKEN=$TOKEN"
echo "NEXUS_DEMO_PROJECT_ID=$PROJECT_ID"
echo "NEXUS_DEMO_USER_ID=$USER_ID"
echo "NEXUS_DEMO_SESSION_ID=${IMAGE_SESSION_ID:-$(jq -r .sync <<< "$SESSIONS")}"
