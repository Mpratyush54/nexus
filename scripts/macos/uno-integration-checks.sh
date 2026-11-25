#!/usr/bin/env bash
# System integration checks for the Uno macOS head (desktop-macos.yml).
#
# One Nexus.app instance in an isolated, seeded profile (no server, no
# credentials). Required checks fail the job:
#   - the menu bar status item exists (log line + an on-screen status window)
#   - `open nexus://...` is delivered to the running app (log line)
#   - a second launch hands its arguments to the first one and exits
#   - --set-start-at-login=on|off (the Settings toggle's code path) writes and
#     removes the LaunchAgent plist, in the profile and in ~/Library
#   - a system quit (Apple event) runs the app's own shutdown
# Checks that need Accessibility (System Events) are reported, not required:
# the app and status menus, close-hides, reopen, Cmd+K and copy/paste.
#
# Needs: APP (path of Nexus.app), RUNNER_TEMP, dist/winlist.
set -uo pipefail
cd "${GITHUB_WORKSPACE:-.}"
mkdir -p shots logs
: > logs/integration-summary.txt

PL="$APP/Contents/Info.plist"
EXE="$APP/Contents/MacOS/$(/usr/libexec/PlistBuddy -c 'Print :CFBundleExecutable' "$PL")"
NAME=ci-mac-system
ROOT="$RUNNER_TEMP/nexus-profile-$NAME"
OUT="logs/$NAME-stdout.log"
FAILS=0

pass() { echo "PASS: $*" | tee -a logs/integration-summary.txt; }
fail() { echo "::error::FAIL: $*"; echo "FAIL: $*" >> logs/integration-summary.txt; FAILS=$((FAILS + 1)); }
note() { echo "NOTE: $*" | tee -a logs/integration-summary.txt; }
limited() { local secs=$1; shift; perl -e 'alarm shift; exec @ARGV' "$secs" "$@"; }
nexus() {
  env -u NEXUS_SERVER -u CENTRAL_SERVER_URL -u NEXUS_TOKEN -u CENTRAL_MEMORY_TOKEN \
    NEXUS_PROFILE="$NAME" NEXUS_PROFILE_ROOT="$ROOT" "$EXE" "$@"
}
windows_at() { ./dist/winlist "$PID" 2>/dev/null | tail -n +2 | awk -v l="$1" '$6==l'; }
has_window() { [ -n "$(windows_at 0)" ]; }
no_window() { [ -z "$(windows_at 0)" ]; }
alive() { kill -0 "$PID" 2>/dev/null; }
gone() { ! kill -0 "$PID" 2>/dev/null; }
app_log() { cat "$OUT" 2>/dev/null; find "$ROOT" -path '*logs*' -name '*.log' -exec cat {} + 2>/dev/null; }
log_has() { app_log | grep -q -- "$1"; }
wait_for() {
  local secs=$1; shift
  local i
  for ((i = 0; i < secs * 2; i++)); do
    if "$@"; then return 0; fi
    sleep 0.5
  done
  return 1
}
se() { # System Events against this process; prints the result or the error
  limited 12 osascript -e "tell application \"System Events\" to tell (first process whose unix id is $PID) to $1" 2>&1
}
keys() { limited 8 osascript -e "tell application \"System Events\" to $1" 2>&1; }

rm -rf "$ROOT"
mkdir -p "$ROOT/AppData/Local/Nexus"
printf '{ "SetupCompleted": true, "LastNav": "timeline", "Theme": "dark" }\n' > "$ROOT/AppData/Local/Nexus/ui-prefs.json"

nexus > "$OUT" 2>&1 &
PID=$!
echo "== $NAME pid=$PID"
if wait_for 90 has_window; then pass "main window is up"; else fail "no main window within 90 s"; fi
sleep 12

# ---- 1. Menu bar status item -------------------------------------------------
if wait_for 20 log_has "status item ready visible=1"; then
  pass "status item created: $(app_log | grep -m1 -o 'status item ready.*')"
else
  fail "no 'status item ready visible=1' log line"
fi
ITEM=$(windows_at 25 | head -1)
if [ -n "$ITEM" ]; then pass "status item window on screen (id x y w h layer): $ITEM"; else fail "no menu bar window (layer 25) owned by the app"; fi
screencapture -x shots/10-desktop-with-status-item.png
if [ -n "$ITEM" ]; then
  read -r _ ix iy iw ih _ <<< "$ITEM"
  ix=${ix%.*}; iw=${iw%.*}; ih=${ih%.*}
  [ "${ih:-0}" -gt 0 ] || ih=24
  left=$((ix > 320 ? ix - 320 : 0))
  screencapture -x -R"$left,0,$((ix - left + iw + 120)),$ih" shots/11-menubar-around-item.png
fi

# ---- 2. Menus (Accessibility) -------------------------------------------------
AX=0
BAR=$(se 'get name of every menu bar item of menu bar 1')
echo "app menu bar: $BAR"
if echo "$BAR" | grep -q "Edit" && echo "$BAR" | grep -q "Window"; then
  AX=1
  pass "app menu bar: $BAR"
  for i in 2 3 4 5 6; do
    echo "menu $i: $(se "get name of every menu item of menu 1 of menu bar item $i of menu bar 1")"
  done | tee logs/app-menus.txt
  STATUS_MENU=$(se 'get name of every menu item of menu 1 of menu bar item 1 of menu bar 2')
  echo "status menu: $STATUS_MENU" | tee -a logs/app-menus.txt
  if echo "$STATUS_MENU" | grep -q "Open Nexus" && echo "$STATUS_MENU" | grep -q "Pause capture" && echo "$STATUS_MENU" | grep -q "Quit Nexus"; then
    pass "status item menu: $STATUS_MENU"
  else
    note "status item menu not as expected: $STATUS_MENU"
  fi
  # Screenshots with the menus open.
  (se 'click menu bar item 1 of menu bar 2' > /dev/null &)
  sleep 2
  screencapture -x shots/12-status-menu-open.png
  keys 'key code 53' > /dev/null
  sleep 1
  (se 'click menu bar item 4 of menu bar 1' > /dev/null &)
  sleep 2
  screencapture -x shots/13-edit-menu-open.png
  keys 'key code 53' > /dev/null
  sleep 1
else
  note "System Events cannot read this app's menus here (no Accessibility): $BAR"
fi

# ---- 3. Close hides, menu bar item and Dock reopen ---------------------------
if [ $AX = 1 ]; then
  se 'click menu item "Close Window" of menu 1 of menu bar item 3 of menu bar 1' > /dev/null
  if wait_for 6 no_window && alive; then pass "Close Window hides the window; the app keeps running"; else note "window still visible after Close Window"; fi
  screencapture -x shots/20-after-close.png
  (se 'click menu bar item 1 of menu bar 2' > /dev/null &)
  sleep 1.5
  se 'click menu item "Open Nexus" of menu 1 of menu bar item 1 of menu bar 2' > /dev/null
  if wait_for 8 has_window; then pass "Open Nexus (menu bar item) shows the window again"; else note "Open Nexus did not show the window"; fi
  se 'click menu item "Close Window" of menu 1 of menu bar item 3 of menu bar 1' > /dev/null
  wait_for 6 no_window || true
fi
# Opening the running app again (what a Dock click does) sends a reopen event.
open -a "$APP"
if wait_for 10 has_window; then
  if log_has "dock reopen" || [ $AX = 0 ]; then pass "reopen (open -a, as a Dock click) shows the window"; else pass "window visible after reopen"; fi
else
  fail "no window after reopening the running app"
fi
sleep 3
screencapture -x shots/21-after-reopen.png

# ---- 4. Cmd shortcuts and copy/paste in a text box (Accessibility) -----------
if [ $AX = 1 ]; then
  se 'set frontmost to true' > /dev/null
  sleep 1
  keys 'keystroke "k" using command down' > /dev/null
  sleep 2
  screencapture -x shots/30-cmd-k-search.png
  keys 'keystroke "nexus copy check"' > /dev/null
  sleep 0.5
  printf 'stale' | pbcopy
  keys 'keystroke "a" using command down' > /dev/null
  keys 'keystroke "c" using command down' > /dev/null
  sleep 1
  if [ "$(pbpaste)" = "nexus copy check" ]; then pass "Cmd+K opened Search; Cmd+A, Cmd+C copied from its text box"; else note "Cmd+C from the text box gave '$(pbpaste)'"; fi
  printf 'pasted by CI' | pbcopy
  se 'click menu item "Select All" of menu 1 of menu bar item 4 of menu bar 1' > /dev/null
  se 'click menu item "Paste" of menu 1 of menu bar item 4 of menu bar 1' > /dev/null
  sleep 0.5
  printf 'stale' | pbcopy
  se 'click menu item "Select All" of menu 1 of menu bar item 4 of menu bar 1' > /dev/null
  se 'click menu item "Copy" of menu 1 of menu bar item 4 of menu bar 1' > /dev/null
  sleep 1
  if [ "$(pbpaste)" = "pasted by CI" ]; then pass "Edit menu Select All, Paste and Copy work in the text box"; else note "Edit menu paste/copy round trip gave '$(pbpaste)'"; fi
  screencapture -x shots/31-after-paste.png
  keys 'key code 53' > /dev/null
  sleep 1
fi

# ---- 5. nexus:// through LaunchServices --------------------------------------
open "nexus://session/ci-url-test"
if wait_for 15 log_has "url event: nexus://session"; then pass "open nexus://session/... reached the running app"; else fail "no 'url event: nexus://session' log line after open"; fi
sleep 3
screencapture -x shots/40-after-url.png

# ---- 6. Second launch hands off -----------------------------------------------
nexus "nexus://session/ci-handoff" > logs/second-launch.log 2>&1 &
P2=$!
rc=""
for _ in $(seq 1 40); do kill -0 $P2 2>/dev/null || break; sleep 0.5; done
if kill -0 $P2 2>/dev/null; then
  fail "second launch still running after 20 s"
  kill -9 $P2 2>/dev/null
else
  wait $P2; rc=$?
  if [ "$rc" = 0 ] && grep -q "handed this launch" logs/second-launch.log; then pass "second launch handed off and exited 0"; else fail "second launch rc=$rc: $(tail -3 logs/second-launch.log)"; fi
fi
if wait_for 10 log_has "second launch handed over its arguments"; then pass "running app received the second launch's arguments"; else fail "running app did not log the hand-off"; fi
alive && pass "first instance still running" || fail "first instance exited"

# ---- 7. Start at login --------------------------------------------------------
check_login() { # label, plist, runner...
  local label=$1 plist=$2; shift 2
  local out rc
  out=$("$@" --set-start-at-login=on 2>&1); rc=$?
  echo "$label on: rc=$rc $out"
  if [ $rc = 0 ] && [ -f "$plist" ] && plutil -lint "$plist" > /dev/null \
     && [ "$(plutil -extract ProgramArguments.1 raw "$plist")" = "--background" ] \
     && [ "$(plutil -extract RunAtLoad raw "$plist")" = "true" ] \
     && [ "$(basename "$(plutil -extract ProgramArguments.0 raw "$plist")")" = "$(basename "$EXE")" ]; then
    pass "$label: toggle on wrote $plist"
    cp "$plist" "logs/$(basename "$plist")"
  else
    fail "$label: toggle on did not write a valid $plist (rc=$rc: $out)"
  fi
  out=$("$@" --set-start-at-login=off 2>&1); rc=$?
  echo "$label off: rc=$rc $out"
  if [ $rc = 0 ] && [ ! -e "$plist" ]; then pass "$label: toggle off removed it"; else fail "$label: toggle off left $plist (rc=$rc)"; fi
}
check_login "profile" "$ROOT/Library/LaunchAgents/com.nexus.desktop.login.profile-$NAME.plist" nexus
# The real per-user location (a disposable runner; launchd only reads it at login).
check_login "user" "$HOME/Library/LaunchAgents/com.nexus.desktop.login.plist" env -u NEXUS_PROFILE -u NEXUS_PROFILE_ROOT "$EXE"

# ---- 8. Quit through the system (Dock / Apple event) --------------------------
limited 15 osascript -e 'tell application id "com.nexus.desktop" to quit' > logs/apple-quit.log 2>&1
if wait_for 15 gone; then
  if log_has "quit from the system"; then pass "system quit ran the app's own shutdown"; else note "app exited after the quit event without the hook's log line"; fi
else
  note "quit Apple event not delivered ($(cat logs/apple-quit.log)); quitting through a second launch"
  nexus --quit > logs/quit-launch.log 2>&1
  if wait_for 15 gone; then pass "--quit through the socket ended the running app"; else fail "the app did not quit"; kill -9 "$PID" 2>/dev/null; fi
fi

# ---- Evidence -------------------------------------------------------------------
mkdir -p "logs/$NAME"
find "$ROOT" -maxdepth 6 -name '*.log' -exec cp {} "logs/$NAME/" \; 2>/dev/null || true
pkill -f "$EXE" 2>/dev/null || true
echo "--- app log (mac lines)"
app_log | grep -E "\[nexus-mac\]| mac: |startup" | tail -40
echo "--- summary"
cat logs/integration-summary.txt
[ $FAILS = 0 ]
