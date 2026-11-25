#!/usr/bin/env bash
# Production-readiness walk of the macOS app (desktop-macos.yml, mac-audit).
#
# Nexus installed from the DMG into /Applications, signed in (through a
# throwaway profile's config.json) to a loopback demo server with fictional
# data (scripts/macos/demo-server.sh + demo-seed.sh). Every screen is
# screenshotted in Light and Dark: the welcome and setup guide, sign-in,
# Home and the project switcher, the chat list and an open chat, the
# composer (agent picker, attach, pasted image), Memory and the Graph,
# Agents, every Settings section, Report a problem, offline and back
# online, the menu bar and app menus, Cmd shortcuts, close and reopen.
# It also records launch time and memory, the Accessibility tree (what
# VoiceOver sees) and which agents the resolver finds when Nexus starts
# with Finder's PATH. Evidence only: most checks note rather than fail,
# a crash or a missing window fails.
#
# Needs: APP (installed Nexus.app), RUNNER_TEMP, dist/winlist, dist/pointer,
# NEXUS_DEMO_SERVER (from demo-server.sh).
set -uo pipefail
cd "${GITHUB_WORKSPACE:-.}"
S=shots-audit
L=logs-audit
mkdir -p "$S" "$L"
SUMMARY=$L/summary.txt
: > "$SUMMARY"
FAILS=0
pass() { echo "PASS $(date +%T): $*" | tee -a "$SUMMARY"; }
fail() { echo "::error::FAIL $(date +%T): $*"; echo "FAIL $(date +%T): $*" >> "$SUMMARY"; FAILS=$((FAILS + 1)); }
note() { echo "NOTE $(date +%T): $*" | tee -a "$SUMMARY"; }
limited() { local secs=$1; shift; perl -e 'alarm shift; exec @ARGV' "$secs" "$@"; }
wait_for() {
  local secs=$1; shift
  local i
  for ((i = 0; i < secs * 2; i++)); do
    if "$@"; then return 0; fi
    sleep 0.5
  done
  return 1
}
now_ms() { perl -MTime::HiRes=time -e 'printf "%d\n", time*1000'; }

PL="$APP/Contents/Info.plist"
EXE="$APP/Contents/MacOS/$(/usr/libexec/PlistBuddy -c 'Print :CFBundleExecutable' "$PL")"
PID=""
NAME=""
ROOT=""

keys() { limited 10 osascript -e "tell application \"System Events\" to $1" 2>&1; }
se() { limited 15 osascript -e "tell application \"System Events\" to tell (first process whose unix id is $PID) to $1" 2>&1; }
front() { se 'set frontmost to true' > /dev/null; sleep 0.6; }
win() { ./dist/winlist "$PID" 2>/dev/null | tail -n +2 | awk '$6==0' | sort -k5 -n -r | head -1; }
has_window() { [ -n "$(win)" ]; }
alive() { kill -0 "$PID" 2>/dev/null; }
# Window-relative click: x from the left edge, y from the top (points).
# Negative x counts from the right edge.
click() {
  local b x y
  b=$(win); [ -n "$b" ] || return 1
  set -- $b "$1" "$2"
  x=$7; y=$8
  [ "$x" -lt 0 ] && x=$(( $4 + x ))
  ./dist/pointer click $(( $2 + x )) $(( $3 + y ))
}
# Click a fraction of the window: click_frac 0.5 0.9
click_frac() {
  local b
  b=$(win); [ -n "$b" ] || return 1
  set -- $b "$1" "$2"
  ./dist/pointer click "$(perl -e "print int($2 + $4 * $7)")" "$(perl -e "print int($3 + $5 * $8)")"
}
scroll_at() { # fx fy lines
  local b
  b=$(win); [ -n "$b" ] || return 1
  set -- $b "$1" "$2" "$3"
  ./dist/pointer scroll "$(perl -e "print int($2 + $4 * $7)")" "$(perl -e "print int($3 + $5 * $8)")" "$9"
}
shot() { # name: the window alone (with shadow cut) and the whole screen
  local b wid
  b=$(win)
  wid=$(echo "$b" | cut -d' ' -f1)
  if [ -n "$wid" ]; then screencapture -x -o -l "$wid" "$S/$1.png" || true; else screencapture -x "$S/$1-noscreen.png"; fi
}
shot_screen() { screencapture -x "$S/$1.png" || true; }
ax_dump() { # name: what Accessibility (VoiceOver, System Events) sees in the window
  se 'get entire contents of window 1' > "$L/ax-$1.txt" 2>&1 || true
  echo "ax $1: $(wc -c < "$L/ax-$1.txt") bytes, $(tr ',' '\n' < "$L/ax-$1.txt" | grep -c 'button\|text field\|static text' ) elements"
}
rss_mb() { ps -o rss= -p "$PID" 2>/dev/null | awk '{printf "%d", $1/1024}'; }

# A profile: 0 = fresh (first run), 1 = setup done but signed out,
# 2 = signed in to the demo server.
profile() { # name state theme nav
  NAME=$1
  ROOT="$RUNNER_TEMP/nexus-audit-$1"
  rm -rf "$ROOT"
  mkdir -p "$ROOT/AppData/Local/Nexus"
  if [ "$2" != 0 ]; then
    printf '{ "SetupCompleted": true, "LastNav": "%s", "Theme": "%s" }\n' "$4" "$3" > "$ROOT/AppData/Local/Nexus/ui-prefs.json"
  fi
  if [ "$2" = 2 ]; then
    NEXUS_DEMO_THEME=$3 NEXUS_DEMO_LAST_NAV=$4 bash scripts/macos/demo-seed.sh "$ROOT" > "$L/seed-$1.log" 2>&1 \
      || note "profile seed for $1 failed: $(tail -3 "$L/seed-$1.log")"
    grep -m1 '^NEXUS_DEMO_SESSION_ID=' "$L/seed-$1.log" | cut -d= -f2 > "$ROOT/demo-chat-id" || true
  fi
  # Fake agents where the common Mac installers put them, so the resolver
  # and the agent picker have something to find with Finder's PATH.
  mkdir -p "$ROOT/.local/bin" "$ROOT/.npm-global/bin"
  printf '#!/bin/sh\necho "2.0.0 (Claude Code)"\n' > "$ROOT/.local/bin/claude"
  printf '#!/usr/bin/env node\nconsole.log("codex-cli 0.50.0")\n' > "$ROOT/.npm-global/bin/codex"
  chmod +x "$ROOT/.local/bin/claude" "$ROOT/.npm-global/bin/codex"
}

# Launched the way Finder does: launchd's minimal PATH, no shell variables.
launch() { # extra args...
  local t0 t1
  t0=$(now_ms)
  # HOME and the AppData folders go in the real environment too: .NET's
  # SetEnvironmentVariable does not reach the in-process Go core on macOS.
  env -i HOME="$ROOT" USERPROFILE="$ROOT" APPDATA="$ROOT/AppData/Roaming" LOCALAPPDATA="$ROOT/AppData/Local" \
    CENTRAL_MEMORY_CONFIG_DIR="$ROOT/Library/Application Support/central-memory" \
    USER="$USER" TMPDIR="$TMPDIR" PATH=/usr/bin:/bin:/usr/sbin:/sbin LANG=en_US.UTF-8 \
    NEXUS_PROFILE="$NAME" NEXUS_PROFILE_ROOT="$ROOT" "$EXE" "$@" > "$L/$NAME-stdout.log" 2>&1 &
  PID=$!
  if wait_for 90 has_window; then
    t1=$(now_ms)
    echo "$NAME window_ms=$((t1 - t0))" >> "$L/perf.txt"
    sleep 6
    front
    sleep 2
    echo "$NAME rss_mb_after_launch=$(rss_mb)" >> "$L/perf.txt"
    return 0
  fi
  fail "$NAME: no window within 90 s"
  return 1
}
stop() {
  [ -n "$PID" ] || return 0
  if alive; then
    echo "$NAME rss_mb_at_end=$(rss_mb)" >> "$L/perf.txt"
    kill "$PID" 2>/dev/null; wait_for 8 bash -c "! kill -0 $PID 2>/dev/null" || kill -9 "$PID" 2>/dev/null
  else
    fail "$NAME: Nexus exited on its own (crash?)"
  fi
  mkdir -p "$L/$NAME"
  find "$ROOT" -maxdepth 7 -name '*.log' -exec cp {} "$L/$NAME/" \; 2>/dev/null || true
  PID=""
}
appearance() { # light|dark: the macOS appearance (the "same as macOS" theme)
  local dark=false
  [ "$1" = dark ] && dark=true
  keys "tell appearance preferences to set dark mode to $dark" > /dev/null
  sleep 1
}

echo "== environment"
sw_vers | tee "$L/machine.txt"
system_profiler SPDisplaysDataType 2>/dev/null | grep -E 'Resolution|Retina|UI Looks' | tee -a "$L/machine.txt"
command -v node | tee -a "$L/machine.txt" || true

# Clicks the first element whose Accessibility name is $1 (what VoiceOver
# reads), at its centre; prints "x y" or "missing".
ax_click() {
  local at
  at=$(limited 60 osascript - "$PID" "$1" <<'OSA' 2>&1
on run argv
  set pid to (item 1 of argv) as integer
  set wanted to item 2 of argv
  tell application "System Events" to tell (first process whose unix id is pid)
    repeat with e in (entire contents of window 1)
      try
        if (name of e as text) is wanted then
          set p to position of e
          set z to size of e
          return ((item 1 of p) + (item 1 of z) div 2) & " " & ((item 2 of p) + (item 2 of z) div 2) as text
        end if
      end try
    end repeat
  end tell
  return "missing"
end run
OSA
)
  echo "ax_click '$1': $at" >> "$L/clicks.txt"
  case "$at" in
    missing*|*rror*|"") note "no element named '$1' to click"; return 1 ;;
  esac
  ./dist/pointer click ${at% *} ${at#* }
}
nav() { ax_click "$1" || true; sleep 4; }
dismiss() { keys 'key code 53' > /dev/null; sleep 0.8; }

# ---- 1. First run: welcome, setup guide, sign-in -------------------------------
for theme in light dark; do
  appearance $theme
  profile "fresh-$theme" 0 system timeline
  if launch; then
    shot "01-welcome-$theme"
    ax_dump "first-run-$theme"
    # "Get started": centred, 165 pt below the middle (Platform/MacWelcome.cs).
    b=$(win); set -- $b
    ./dist/pointer click $(( $2 + $4 / 2 )) $(( $3 + $5 / 2 + 165 )); sleep 4
    shot "02-setup-signin-$theme"
    ax_dump "setup-$theme"
    stop
  fi
done
appearance light

# Setup done, never signed in.
for theme in light dark; do
  profile "signedout-$theme" 1 "$theme" timeline
  if launch; then sleep 8; shot "03-signed-out-$theme"; stop; fi
done

# ---- 2. Signed in: every page in both themes, driven like a person ------------
for theme in light dark; do
  profile "walk-$theme" 2 "$theme" home
  launch || continue
  sleep 8   # first fetch from the server
  shot "10-home-$theme"
  ax_dump "home-$theme"
  # Project switcher (Cmd+P), then pick "Orbit Notes".
  keys 'keystroke "p" using command down' > /dev/null; sleep 2; shot "11-project-switcher-$theme"
  dismiss; shot "11b-switcher-after-esc-$theme"
  click 650 110; sleep 1
  # Search (Cmd+K).
  keys 'keystroke "k" using command down' > /dev/null; sleep 2
  keys 'keystroke "sync"' > /dev/null; sleep 3; shot "12-search-$theme"
  dismiss; sleep 1
  # The chat list: open the first chat by its name.
  ax_click "Fix sync conflict when two devices edit the same note" && { sleep 6; shot "13-chat-open-$theme"; ax_dump "chat-$theme"; }
  scroll_at 0.6 0.45 -12; sleep 1.5; shot "14-chat-scrolled-$theme"
  # The composer: agent picker, typing, a pasted image.
  ax_click "Ask anything…" || click_frac 0.6 0.82
  keys 'keystroke "Hello from the macOS audit"' > /dev/null; sleep 1; shot "15-composer-typed-$theme"
  screencapture -x -R0,0,320,200 "$RUNNER_TEMP/clip.png"
  osascript -e "set the clipboard to (read (POSIX file \"$RUNNER_TEMP/clip.png\") as «class PNGf»)" > /dev/null 2>&1
  keys 'keystroke "v" using command down' > /dev/null; sleep 4; shot "16-composer-pasted-$theme"
  ax_click "Claude Code" && { sleep 2; shot "17-agent-picker-$theme"; dismiss; }
  # New chat (Cmd+N).
  keys 'keystroke "n" using command down' > /dev/null; sleep 3; shot "18-new-chat-$theme"
  # Memory, the graph, Agents, Inbox, Settings.
  nav "Memory"; shot "20-memory-$theme"; ax_dump "memory-$theme"
  ax_click "Graph" && { sleep 6; shot "21-graph-$theme"; ax_dump "graph-$theme"; }
  nav "Agents"; shot "22-agents-$theme"; ax_dump "agents-$theme"
  nav "Chats sent to you"; shot "23-inbox-$theme"
  nav "Settings"; shot "24-settings-$theme"; ax_dump "settings-$theme"
  for i in 1 2 3 4 5 6 7 8 9 10; do scroll_at 0.65 0.6 -8; sleep 1; shot "25-settings-$i-$theme"; done
  nav "Home"
  stop
done

# ---- 3. One long session: open a chat, menus, report, offline/online ----------
profile "session" 2 dark timeline
CHAT_ID=$(cat "$ROOT/demo-chat-id" 2>/dev/null || true)
if [ -n "$CHAT_ID" ]; then launch "nexus://session/$CHAT_ID"; else launch; fi
if [ -n "$PID" ]; then
  sleep 6
  shot "20-open-chat-dark"
  ax_dump "open-chat"
  scroll_at 0.6 0.5 -15; sleep 1; shot "21-open-chat-scrolled-dark"
  # Menu bar: the app's own menus and the status item.
  for i in 2 3 4 5 6; do
    echo "menu $i: $(se "get name of every menu item of menu 1 of menu bar item $i of menu bar 1")"
  done | tee "$L/app-menus.txt"
  echo "status menu: $(se 'get name of every menu item of menu 1 of menu bar item 1 of menu bar 2')" | tee -a "$L/app-menus.txt"
  (se 'click menu bar item 2 of menu bar 1' > /dev/null &); sleep 2; shot_screen "22-app-menu"; keys 'key code 53' > /dev/null; sleep 1
  (se 'click menu bar item 1 of menu bar 2' > /dev/null &); sleep 2; shot_screen "23-status-menu"; keys 'key code 53' > /dev/null; sleep 1
  # Report a problem, from the status item.
  (se 'click menu bar item 1 of menu bar 2' > /dev/null &); sleep 1.5
  se 'click (first menu item of menu 1 of menu bar item 1 of menu bar 2 whose name starts with "Report")' > /dev/null
  sleep 3; shot "24-report-problem-dark"; shot_screen "24-report-problem-screen"
  ax_dump "report"
  keys 'key code 53' > /dev/null; sleep 1
  # Offline: the server goes away; then comes back.
  if [ -n "${NEXUS_DEMO_SERVER_PID:-}" ]; then
    kill -STOP "$NEXUS_DEMO_SERVER_PID"
    note "server paused (offline)"
    sleep 40; shot "25-offline-dark"
    kill -CONT "$NEXUS_DEMO_SERVER_PID"
    sleep 40; shot "26-back-online-dark"
  else
    note "no NEXUS_DEMO_SERVER_PID: offline check skipped"
  fi
  # Close (Cmd+W) hides; reopen through the Dock event.
  front
  keys 'keystroke "w" using command down' > /dev/null; sleep 2
  if has_window; then note "Cmd+W left the window open"; else pass "Cmd+W hid the window"; fi
  open -a "$APP"; sleep 4
  if has_window; then pass "reopen shows the window"; shot "27-reopened-dark"; else fail "no window after reopen"; fi
  # Cmd+, opens Settings.
  keys 'keystroke "," using command down' > /dev/null; sleep 2; shot "28-cmd-comma-dark"
  echo "session rss_mb_after_walk=$(rss_mb)" >> "$L/perf.txt"
  top -l 2 -pid "$PID" -stats pid,cpu,mem 2>/dev/null | tail -2 >> "$L/perf.txt" || true
  stop
fi

# ---- Evidence ------------------------------------------------------------------
for f in ~/Library/Logs/DiagnosticReports/*; do case "$f" in *Nexus*) cp "$f" "$L/" ;; esac; done 2>/dev/null || true
echo "--- perf"; cat "$L/perf.txt"
echo "--- summary"; cat "$SUMMARY"
[ $FAILS = 0 ]
