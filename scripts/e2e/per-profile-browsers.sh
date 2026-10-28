#!/usr/bin/env bash
# Two real Chromium profiles, three monoagent profiles, one private bridge.
# Proves: each profile's command lands in its own browser, both at once, a
# profile with no browser gets the explanatory error, and several profiles
# can share one browser. Never touches the
# real HOME, port 9222, or the user's browsers.
set -euo pipefail

REPO="$(cd "$(dirname "$0")/../.." && pwd)"
W="${W:-$HOME/scratch/per-profile-e2e-$(date +%Y%m%d-%H%M%S)}"
H="$W/home"
PORT=9232
CHROMIUM="${CHROMIUM:-chromium}"
export TMPDIR="$HOME/scratch/agent-tmp" GOTMPDIR="$HOME/scratch/agent-tmp"
mkdir -p "$W" "$H" "$W/site" "$TMPDIR"

CLI="$W/monoagentcli"
(cd "$REPO" && go build -tags devaccount -o "$CLI" ./cmd/monoagentcli)
# Keep this devaccount build independent of the production enforcement date.
export MONOAGENT_DEV_ENFORCE_FROM=2999-01-01T00:00:00Z
# shellcheck source=../lib/mono.sh
. "$REPO/scripts/lib/mono.sh"
mc() { HOME="$H" MONOAGENT_EXTENSION_PORT="$PORT" mono_guard "$CLI" "$@"; }

cleanup() {
  for f in "$W"/*.pid; do [ -f "$f" ] && kill "$(cat "$f")" 2>/dev/null || true; done
}
trap cleanup EXIT

# Two profiles, plus one with no browser at all.
WORK=$(mc --json profile create Work | jq -r .id)
HOMEP=$(mc --json profile create Personal | jq -r .id)
SOLO=$(mc --json profile create Solo | jq -r .id)

# A page per browser, so a capture says which browser it came from.
echo '<title>work page</title><h1>work</h1>' > "$W/site/work.html"
echo '<title>personal page</title><h1>personal</h1>' > "$W/site/personal.html"
python3 -m http.server 9311 --bind 127.0.0.1 --directory "$W/site" >/dev/null 2>&1 & echo $! > "$W/site.pid"

# Started directly (not through mc) so $! is the bridge itself and the trap can stop it.
HOME="$H" MONOAGENT_EXTENSION_PORT="$PORT" "$CLI" extension serve >"$W/bridge.log" 2>&1 & echo $! > "$W/bridge.pid"
for _ in $(seq 50); do curl -sf "http://127.0.0.1:$PORT/monoagent/health" >/dev/null && break; sleep 0.2; done
TOKEN=$(cat "$H/.monoagent/extension.token")

launch() { # name debugPort url
  "$CHROMIUM" --user-data-dir="$W/ud-$1" --no-first-run --no-default-browser-check \
    --load-extension="$REPO/chrome-extension" --remote-debugging-port="$2" --remote-allow-origins='*' ${HEADLESS:---headless=new} --disable-features=DisableLoadExtensionCommandLineSwitch "$3" \
    >"$W/$1.log" 2>&1 & echo $! > "$W/$1.pid"
}
launch work 9301 "http://127.0.0.1:9311/work.html"
launch personal 9302 "http://127.0.0.1:9311/personal.html"

node "$REPO/scripts/e2e/set-extension-storage.mjs" 9301 \
  "{\"wsUrl\":\"ws://127.0.0.1:$PORT/monoagent\",\"pairingToken\":\"$TOKEN\",\"boundProfile\":\"$WORK\",\"browserLabel\":\"E2E Work\"}"
node "$REPO/scripts/e2e/set-extension-storage.mjs" 9302 \
  "{\"wsUrl\":\"ws://127.0.0.1:$PORT/monoagent\",\"pairingToken\":\"$TOKEN\",\"boundProfile\":\"$HOMEP\",\"browserLabel\":\"E2E Personal\"}"

# Both browsers attached and bound.
for _ in $(seq 100); do
  n=$(mc --json extension browsers | jq '[.browsers[] | select(.profile_id != "")] | length')
  [ "$n" = 2 ] && break; sleep 0.3
done
mc extension browsers
[ "$n" = 2 ] || { echo "FAIL: browsers did not both bind"; exit 1; }

# 1. Parallel: each profile captures its own browser's page, at the same time.
mc --profile Work --json capture page --formats mhtml --out "$W/inbox-work" >"$W/cap-work.json" & A=$!
mc --profile Personal --json capture page --formats mhtml --out "$W/inbox-personal" >"$W/cap-personal.json" & B=$!
wait $A; wait $B
grep -q work.html <(jq -r .meta.url "$W/cap-work.json") || { echo "FAIL: Work captured $(jq -r .meta.url "$W/cap-work.json")"; exit 1; }
grep -q personal.html <(jq -r .meta.url "$W/cap-personal.json") || { echo "FAIL: Personal captured $(jq -r .meta.url "$W/cap-personal.json")"; exit 1; }
echo "PASS: each profile captured in its own browser, in parallel"

# 2. A profile with no browser and no default browser gets the fix, not a random browser.
if out=$(mc --profile Solo capture page --formats mhtml --timeout 5s 2>&1); then
  echo "FAIL: Solo captured with no browser of its own: $out"; exit 1
fi
grep -q "no browser is set up for profile" <<<"$out" || { echo "FAIL: unexpected error: $out"; exit 1; }
echo "PASS: Solo gets the no-browser explanation"

# 3. Rebind from the CLI: Personal's browser becomes a default browser, and Solo can use it.
mc extension unbind "E2E Personal"
mc --profile Solo --json capture page --formats mhtml --out "$W/inbox-solo" >"$W/cap-solo.json"
grep -q personal.html <(jq -r .meta.url "$W/cap-solo.json") || { echo "FAIL: Solo did not use the default browser"; exit 1; }
echo "PASS: unbind makes a default browser that unbound profiles use"
# 4. Several profiles share one browser: assign Solo to Work's browser.
if out=$(mc extension assign Solo "no-such-browser" 2>&1); then
  echo "FAIL: assigning to an unattached browser succeeded: $out"; exit 1
fi
grep -q "not attached to the bridge" <<<"$out" || { echo "FAIL: unexpected assign error: $out"; exit 1; }
mc extension assign Solo "E2E Work"
for _ in $(seq 50); do
  n=$(mc --json extension browsers | jq --arg w "$WORK" --arg s "$SOLO" '[.browsers[] | select(.label == "E2E Work") | select((.profile_ids | index($w)) and (.profile_ids | index($s)))] | length')
  [ "$n" = 1 ] && break; sleep 0.2
done
[ "$n" = 1 ] || { echo "FAIL: E2E Work does not run both Work and Solo"; mc extension browsers; exit 1; }
mc --profile Solo --json capture page --formats mhtml --out "$W/inbox-solo2" >"$W/cap-solo2.json"
mc --profile Work --json capture page --formats mhtml --out "$W/inbox-work2" >"$W/cap-work2.json"
grep -q work.html <(jq -r .meta.url "$W/cap-solo2.json") || { echo "FAIL: Solo did not run in the shared browser"; exit 1; }
grep -q work.html <(jq -r .meta.url "$W/cap-work2.json") || { echo "FAIL: Work lost its browser to Solo"; exit 1; }
echo "PASS: Work and Solo share one browser"

# 5. unbind with a profile removes only that profile; assign with no browser removes it everywhere.
mc extension unbind "E2E Work" Solo
n=$(mc --json extension browsers | jq --arg w "$WORK" '[.browsers[] | select(.label == "E2E Work") | select(.profile_ids == [$w])] | length')
[ "$n" = 1 ] || { echo "FAIL: unbind <browser> <profile> did not leave only Work"; exit 1; }
mc extension assign Work
n=$(mc --json extension browsers | jq '[.browsers[] | select(.profile_ids != [])] | length')
[ "$n" = 0 ] || { echo "FAIL: assign <profile> did not clear it everywhere"; exit 1; }
echo "PASS: unbind <profile> and assign <profile> remove it"
mono_scan_log "$W/bridge.log"
mono_check_clean
echo "ALL PASS"
