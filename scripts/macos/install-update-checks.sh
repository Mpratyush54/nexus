#!/usr/bin/env bash
# Install and update checks for the macOS download (desktop-macos.yml):
#
#   1. The DMG: mounted and opened in Finder; the window (background, icon
#      layout) is screenshotted in Light and, when possible, Dark mode.
#   2. "Move to Applications": Nexus launched straight from the disk image
#      offers to move itself; pressing the button copies it to /Applications,
#      quits, and the copy relaunches.
#   3. The first-run welcome ("You're all set") from /Applications.
#   4. Self-update, unsigned: the installed version A finds version B in a
#      local feed, downloads it in the background, and after a restart
#      UpdateMac has swapped the bundle: the running app, sq.version and
#      Info.plist all say B, and Nexus shows "Updated to Nexus B".
#
# Needs: DMG_A (Nexus-<A>.dmg), FEED (Velopack folder holding A and B),
# VERSION_A, VERSION_B, BUNDLE_B (CFBundleVersion of B), dist/winlist.
# Everything runs in throwaway profiles on the runner; no server, no network feed.
set -uo pipefail
cd "${GITHUB_WORKSPACE:-.}"
mkdir -p shots-install logs-install
SUMMARY=logs-install/summary.txt
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
keys() { limited 10 osascript -e "tell application \"System Events\" to $1" 2>&1; }
front() { keys "set frontmost of (first process whose unix id is $1) to true" > /dev/null; }
# The primary button of the first-run screen: centred, 165 pt below the
# window's middle (the layout is centred; see Platform/MacWelcome.cs).
click_primary() { # pid
  local b
  b=$(./dist/winlist "$1" 2>/dev/null | tail -n +2 | awk '$6==0' | sort -k5 -n -r | head -1)
  [ -n "$b" ] || return 1
  set -- $b
  ./dist/pointer click $(( $2 + $4 / 2 )) $(( $3 + $5 / 2 + 165 ))
}
scroll_right_pane() { # pid lines
  local b
  b=$(./dist/winlist "$1" 2>/dev/null | tail -n +2 | awk '$6==0' | sort -k5 -n -r | head -1)
  [ -n "$b" ] || return 1
  set -- $b "$2"
  ./dist/pointer scroll $(( $2 + $4 * 3 / 4 )) $(( $3 + $5 / 2 )) "$7"
}
main_window() { ./dist/winlist "$1" 2>/dev/null | tail -n +2 | awk '$6==0' | sort -k5 -n -r | head -1 | cut -d' ' -f1; }
has_window() { [ -n "$(main_window "$1")" ]; }
shot_window() { # pid name
  local wid
  wid=$(main_window "$1")
  screencapture -x "shots-install/$2-screen.png" || true
  if [ -n "$wid" ]; then screencapture -x -o -l "$wid" "shots-install/$2.png" || true; fi
}
INSTALLED=/Applications/Nexus.app
APP_RE='/Nexus\.app/Contents/MacOS/Nexus( |$)'
app_pids() { pgrep -f "$APP_RE" || true; }
installed_pid() { pgrep -f "^$INSTALLED/Contents/MacOS/Nexus( |\$)" | head -1; }
installed_running() { [ -n "$(installed_pid)" ]; }
quit_all() {
  # Only when running: telling a closed app to quit would open it.
  [ -n "$(app_pids)" ] || return 0
  limited 15 osascript -e 'tell application id "com.nexus.desktop" to quit' > /dev/null 2>&1 || true
  wait_for 12 bash -c "! pgrep -f '$APP_RE' > /dev/null" || { pkill -f "$APP_RE"; sleep 2; pkill -9 -f "$APP_RE" || true; }
}
# Launches by `open`, Dock clicks and UpdateMac's relaunch get their
# environment from launchd: the test profile and the local feed go there too.
use_profile() { # name seeded(0/1)
  PROFILE_ROOT="$RUNNER_TEMP/nexus-profile-$1"
  rm -rf "$PROFILE_ROOT"
  mkdir -p "$PROFILE_ROOT/AppData/Local/Nexus"
  if [ "$2" = 1 ]; then
    printf '{ "SetupCompleted": true, "LastNav": "timeline", "Theme": "dark" }\n' > "$PROFILE_ROOT/AppData/Local/Nexus/ui-prefs.json"
  fi
  launchctl setenv NEXUS_PROFILE "$1"
  launchctl setenv NEXUS_PROFILE_ROOT "$PROFILE_ROOT"
  export NEXUS_PROFILE="$1" NEXUS_PROFILE_ROOT="$PROFILE_ROOT"
}
launchctl setenv NEXUS_UPDATE_URL "$FEED"
export NEXUS_UPDATE_URL="$FEED"
unset NEXUS_SERVER CENTRAL_SERVER_URL NEXUS_TOKEN CENTRAL_MEMORY_TOKEN
cleanup() {
  quit_all
  for v in NEXUS_PROFILE NEXUS_PROFILE_ROOT NEXUS_UPDATE_URL; do launchctl unsetenv "$v"; done
  hdiutil detach /Volumes/Nexus -force -quiet 2>/dev/null || true
}
trap cleanup EXIT
rm -rf "$INSTALLED"

# ---- 1. The DMG window
echo "== 1. DMG"
hdiutil attach "$DMG_A" -noverify | tee logs-install/attach.txt
VOL=/Volumes/Nexus
if [ -d "$VOL/Nexus.app" ] && [ -L "$VOL/Applications" ]; then pass "DMG holds Nexus.app and an Applications link"; else fail "DMG layout: $(ls -la "$VOL" 2>&1)"; fi
ls -la "$VOL" "$VOL/.background" > logs-install/dmg-contents.txt 2>&1 || true
test -f "$VOL/.DS_Store" && pass "DMG has a Finder layout (.DS_Store)" || fail "DMG has no .DS_Store"
test -f "$VOL/.VolumeIcon.icns" && pass "DMG has a volume icon" || note "no .VolumeIcon.icns"
open "$VOL"
sleep 5
FINDER=$(pgrep -x Finder | head -1)
./dist/winlist "$FINDER" > logs-install/finder-windows.txt 2>&1 || true
cat logs-install/finder-windows.txt
WID=$(./dist/winlist "$FINDER" 2>/dev/null | tail -n +2 | awk '$6==0 && $4>=600 && $4<=720' | head -1 | cut -d' ' -f1)
screencapture -x shots-install/01-dmg-screen.png || true
if [ -n "$WID" ]; then
  screencapture -x -l "$WID" shots-install/01-dmg-window.png && pass "DMG Finder window captured ($(grep "^$WID " logs-install/finder-windows.txt))"
else
  fail "no Finder window of the DMG's size"
fi
if keys 'tell appearance preferences to set dark mode to true' > /dev/null; then
  sleep 3
  screencapture -x shots-install/02-dmg-screen-dark.png || true
  [ -n "$WID" ] && screencapture -x -l "$WID" shots-install/02-dmg-window-dark.png || true
  keys 'tell appearance preferences to set dark mode to false' > /dev/null
  pass "DMG window captured in Dark mode too"
else
  note "could not switch to Dark mode (no Automation permission); Light only"
fi
keys 'tell application "Finder" to close every window' > /dev/null || true

# ---- 2. Run from the disk image: "Move to Applications"
echo "== 2. Move to Applications"
use_profile ci-mac-move 0
"$VOL/Nexus.app/Contents/MacOS/Nexus" > logs-install/move-stdout.log 2>&1 &
PID=$!
if wait_for 90 has_window "$PID"; then
  sleep 6
  shot_window "$PID" 03-move-offer
  if grep -q "first-run screen: Move" logs-install/move-stdout.log; then pass "launched from the DMG: the Move to Applications offer is shown"; else fail "no Move offer logged (see move-stdout.log)"; fi
  front "$PID"; sleep 1
  click_primary "$PID"   # "Move to Applications"
  if wait_for 60 installed_running; then
    pass "Move to Applications copied Nexus to /Applications and relaunched it from there"
    if wait_for 30 bash -c "! kill -0 $PID 2>/dev/null"; then pass "the disk-image copy quit"; else fail "the disk-image copy is still running"; fi
    xattr -lr "$INSTALLED" 2>/dev/null | grep -c quarantine | xargs -I{} echo "quarantine xattrs on the moved copy: {}" | tee -a "$SUMMARY"
  else
    fail "Move to Applications did not relaunch from /Applications; installing by hand to go on"
    kill "$PID" 2>/dev/null; sleep 2; kill -9 "$PID" 2>/dev/null || true
    ditto "$VOL/Nexus.app" "$INSTALLED"
    open "$INSTALLED"
  fi
else
  fail "no window when launched from the DMG"
  kill -9 "$PID" 2>/dev/null || true
  ditto "$VOL/Nexus.app" "$INSTALLED"
  open "$INSTALLED"
fi

# ---- 3. The welcome, from /Applications
echo "== 3. Welcome"
if wait_for 60 installed_running && wait_for 60 has_window "$(installed_pid)"; then
  sleep 1.2
  shot_window "$(installed_pid)" 04a-welcome-forming
  sleep 5
  shot_window "$(installed_pid)" 04-welcome
  LOGS=$(find "$PROFILE_ROOT" -path '*logs*' -name '*.log' -exec cat {} + 2>/dev/null)
  if echo "$LOGS" | grep -q "first-run screen: Welcome"; then pass "first launch from /Applications shows You're all set"; else fail "no welcome logged from /Applications"; fi
  # "Get started" (focused) leads into the setup guide underneath.
  front "$(installed_pid)"; sleep 1
  click_primary "$(installed_pid)"
  sleep 3
  shot_window "$(installed_pid)" 04b-setup-after-welcome
  LOGS=$(find "$PROFILE_ROOT" -path '*logs*' -name '*.log' -exec cat {} + 2>/dev/null)
  if echo "$LOGS" | grep -q "first-run screen dismissed"; then pass "Get started opens the setup guide"; else note "Get started: no dismiss logged"; fi
else
  fail "Nexus from /Applications showed no window"
fi
sleep 3
if mount | grep -q "/Volumes/Nexus"; then note "the disk image is still mounted"; else pass "the disk image was ejected after the move"; fi
quit_all
hdiutil detach "$VOL" -force -quiet 2>/dev/null || true

# ---- 4. Update A -> B
echo "== 4. Update $VERSION_A -> $VERSION_B"
version_of() { sed -n 's:.*<version>\(.*\)</version>.*:\1:p' "$INSTALLED/Contents/Resources/sq.version" 2>/dev/null | head -1; }
echo "installed: $(version_of), CFBundleVersion $(/usr/libexec/PlistBuddy -c 'Print :CFBundleVersion' "$INSTALLED/Contents/Info.plist")" | tee -a "$SUMMARY"
[ "$(version_of)" = "$VERSION_A" ] && pass "installed version is $VERSION_A" || fail "installed version is $(version_of), expected $VERSION_A"
# A copy dragged from a downloaded DMG carries com.apple.quarantine (cleared
# here by Move to Applications). Either way the update is extracted by
# UpdateMac from a package Nexus downloaded itself, which macOS never
# quarantines, so B opens without Gatekeeper; checked below.
xattr -l "$INSTALLED" | tee -a "$SUMMARY"
use_profile ci-mac-update 1
"$INSTALLED/Contents/MacOS/Nexus" > logs-install/update-a-stdout.log 2>&1 &
PID=$!
profile_logs() { find "$PROFILE_ROOT" -path '*logs*' -name '*.log' -exec cat {} + 2>/dev/null; }
staged() { profile_logs | grep "version $VERSION_B downloaded" > /dev/null; }
if wait_for 150 staged; then
  # Under NEXUS_PROFILE the profile is the home folder, so Velopack's cache is in it.
  pass "background check downloaded $VERSION_B: $(find "$PROFILE_ROOT" "$HOME/Library/Caches/velopack" -name "*$VERSION_B*.nupkg" 2>/dev/null | head -2 | tr '
' ' ')"
else
  fail "no $VERSION_B download logged after 150 s"
fi
grep -h "update" logs-install/update-a-stdout.log | tail -5
if has_window "$PID"; then
  front "$PID"; sleep 1
  keys 'keystroke "," using command down' > /dev/null   # Settings (Updates is in General)
  sleep 4
  scroll_right_pane "$PID" -25   # down to Updates
  sleep 2
  shot_window "$PID" 05-update-before
fi
quit_all
echo "-- restart: Velopack applies $VERSION_B before the UI starts"
"$INSTALLED/Contents/MacOS/Nexus" > logs-install/update-restart-stdout.log 2>&1 &
RPID=$!
swapped() { [ "$(version_of)" = "$VERSION_B" ]; }
if wait_for 120 swapped; then pass "UpdateMac swapped the bundle: sq.version is $VERSION_B"; else fail "bundle still $(version_of) after restart"; fi
if wait_for 60 bash -c "! kill -0 $RPID 2>/dev/null"; then pass "the $VERSION_A process exited to apply the update"; fi
if wait_for 90 installed_running; then
  NEW=$(installed_pid)
  pass "Nexus relaunched after the update (pid $NEW)"
  wait_for 60 has_window "$NEW" || true
  sleep 8
  shot_window "$NEW" 06-update-after
else
  fail "Nexus did not relaunch after the update"
fi
BV=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleVersion' "$INSTALLED/Contents/Info.plist")
[ "$BV" = "$BUNDLE_B" ] && pass "Info.plist CFBundleVersion is $BUNDLE_B (B)" || fail "CFBundleVersion is $BV, expected $BUNDLE_B"
if xattr -p com.apple.quarantine "$INSTALLED" > /dev/null 2>&1; then note "updated bundle carries com.apple.quarantine"; else pass "updated bundle has no quarantine flag (no Gatekeeper prompt for B)"; fi
codesign --verify --deep --strict --verbose=2 "$INSTALLED" > logs-install/codesign-after-update.txt 2>&1 \
  && pass "updated bundle's signature verifies (codesign --verify --deep --strict)" \
  || fail "updated bundle's signature: $(tail -3 logs-install/codesign-after-update.txt | tr '\n' ' ')"
ls -l "$INSTALLED/Contents/MacOS" > logs-install/macos-dir-after-update.txt 2>&1 || true
UPLOGS=$(find "$PROFILE_ROOT" -path '*logs*' -name '*.log' -exec cat {} + 2>/dev/null)
if echo "$UPLOGS" | grep -q "installed version $VERSION_B"; then pass "the running app reports version $VERSION_B"; else fail "no 'installed version $VERSION_B' in the app log"; fi
if echo "$UPLOGS" | grep -q "startup: Nexus $VERSION_B starting"; then pass "the startup log line names the installed version ($VERSION_B)"; else fail "startup log: $(echo "$UPLOGS" | grep 'startup: Nexus' | tail -1)"; fi
quit_all
# If UpdateMac's relaunch didn't carry the profile, show the toast from a launch that does.
if ! [ -f shots-install/06-update-after.png ]; then
  "$INSTALLED/Contents/MacOS/Nexus" > logs-install/update-b-stdout.log 2>&1 &
  PID=$!
  wait_for 60 has_window "$PID" && { sleep 6; shot_window "$PID" 06-update-after; }
  quit_all
fi

cp "$HOME"/Library/Logs/velopack_NexusMac.log logs-install/ 2>/dev/null || true
for p in ci-mac-move ci-mac-update; do
  mkdir -p "logs-install/$p"
  find "$RUNNER_TEMP/nexus-profile-$p" -path '*logs*' -name '*.log' -exec cp {} "logs-install/$p/" \; 2>/dev/null || true
done
echo "== summary"; cat "$SUMMARY"
exit $((FAILS > 0))
