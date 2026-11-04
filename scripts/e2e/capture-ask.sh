#!/usr/bin/env bash
# Save a page from the real side panel, then ask about it — in the right
# profile only. Proves: a capture saved into a profile is indexed by the
# bridge with no Index button, "Ask your brain" in that profile cites it,
# and asking from another profile does not.
#
# Never touches the real HOME, port 9222, or the user's browsers: a scratch
# HOME, a private bridge on 9232 and a headless Chromium with the working
# tree's extension. The page URL has a query string, which monomind
# before 2.18.3 could not index. monomind is whatever is first on PATH
# (MONOMIND_PATH puts a branch build first, for this run only).
set -euo pipefail

# The port override is the operator's alone, and a script must never fall back to the
# real bridge on 9222/9323: clear every marker and run in a private network namespace.
. "$(cd "$(dirname "$0")/../.." && pwd)/scripts/lib/mono.sh"
mono_private_operator_env bash "$0" "$@"

REPO="$(cd "$(dirname "$0")/../.." && pwd)"
W="${W:-$HOME/scratch/capture-ask-e2e-$(date +%Y%m%d-%H%M%S)}"
H="$W/home"
PORT=9232
DEBUG=9303
CHROMIUM="${CHROMIUM:-chromium}"
export TMPDIR="$HOME/scratch/agent-tmp" GOTMPDIR="$HOME/scratch/agent-tmp"
mkdir -p "$W" "$H" "$W/site" "$TMPDIR"
case "$H" in "$HOME"/scratch/*) ;; *) echo "refusing HOME=$H" >&2; exit 97 ;; esac

CLI="$W/monoagentcli"
(cd "$REPO" && go build -tags devaccount -o "$CLI" ./cmd/monoagentcli)
# Keep this devaccount build independent of the production enforcement date.
export MONOAGENT_DEV_ENFORCE_FROM=2999-01-01T00:00:00Z
PATH_FOR_RUN="${MONOMIND_PATH:+$MONOMIND_PATH:}$PATH"
# shellcheck source=../lib/mono.sh
. "$REPO/scripts/lib/mono.sh"
mc() { mono_guard env HOME="$H" PATH="$PATH_FOR_RUN" MONOAGENT_EXTENSION_PORT="$PORT" MONOAGENT_SUMMARY_RUNTIME=off "$CLI" "$@"; }
echo "monomind: $(env HOME="$H" PATH="$PATH_FOR_RUN" monomind --version)"

cleanup() {
  for f in "$W"/*.pid; do [ -f "$f" ] && kill "$(cat "$f")" 2>/dev/null || true; done
}
trap cleanup EXIT

WORK=$(mc --json profile create Work | jq -r .id)
PERSONAL=$(mc --json profile create Personal | jq -r .id)

cat >"$W/site/lighthouse.html" <<'HTML'
<!doctype html><html><head><title>The Dunmore Lighthouse Ledger</title></head><body><article>
<h1>The Dunmore Lighthouse Ledger</h1>
<p>The keeper of the Dunmore lighthouse kept a brass-bound ledger of every ship that passed the headland.</p>
<p>In the winter of 1887 the lamp burned whale oil, and the keeper trimmed the wick every four hours through the night.</p>
<p>The fog signal was a steam siren that sounded two long blasts every ninety seconds whenever visibility dropped below a mile.</p>
</article></body></html>
HTML
python3 -m http.server 9312 --bind 127.0.0.1 --directory "$W/site" >/dev/null 2>&1 & echo $! > "$W/site.pid"

env HOME="$H" PATH="$PATH_FOR_RUN" MONOAGENT_EXTENSION_PORT="$PORT" MONOAGENT_SUMMARY_RUNTIME=off \
  "$CLI" extension serve >"$W/bridge.log" 2>&1 & echo $! > "$W/bridge.pid"
for _ in $(seq 50); do curl -sf "http://127.0.0.1:$PORT/monoagent/health" >/dev/null && break; sleep 0.2; done
TOKEN=$(cat "$H/.monoagent/extension.token")

"$CHROMIUM" --user-data-dir="$W/ud" --no-first-run --no-default-browser-check \
  --load-extension="$REPO/chrome-extension" --remote-debugging-port="$DEBUG" --remote-allow-origins='*' \
  ${HEADLESS:---headless=new} --disable-features=DisableLoadExtensionCommandLineSwitch \
  "http://127.0.0.1:9312/lighthouse.html?edition=1887&lang=en" >"$W/chromium.log" 2>&1 & echo $! > "$W/chromium.pid"

node "$REPO/scripts/e2e/set-extension-storage.mjs" "$DEBUG" \
  "{\"wsUrl\":\"ws://127.0.0.1:$PORT/monoagent\",\"pairingToken\":\"$TOKEN\",\"captureProfile\":\"$WORK\"}"
for _ in $(seq 100); do
  [ "$(mc --json extension browsers | jq '.browsers | length')" -ge 1 ] && break; sleep 0.3
done

drive() { node "$REPO/scripts/e2e/capture-ask.mjs" "$DEBUG" lighthouse.html "$@"; }
Q="how often did the fog signal sound"

drive profile "$WORK"
drive save | tee "$W/save.json"
T0=$(date +%s)

# 1. Indexed automatically: the Work row turns Indexed without any Index call.
for _ in $(seq 120); do
  state=$(mc --profile Work --json profile documents list | jq -r '[.[] | select(.CaptureDir != "")][0] | "\(.Indexed) \(.IndexError)"')
  case "$state" in true*) break ;; esac
  sleep 0.5
done
echo "Work capture row: $state (after $(( $(date +%s) - T0 ))s)"
[[ "$state" == true* ]] || { echo "FAIL: capture was not indexed: $state"; tail -20 "$W/bridge.log"; exit 1; }
echo "PASS: the capture was indexed by the bridge, no Index button"

# 2. Ask in Work: a cited passage from the page.
drive ask "$Q" | tee "$W/ask-work.json"
# The quote is the passage's opening; the cite URL's text fragment spans
# the passage, so between them the fog-signal sentence must be cited.
jq -e '.answers | map(select((.quote + " " + (.url | gsub("%20"; " "))) | test("ninety seconds|fog signal|two long blasts"; "i"))) | length > 0' "$W/ask-work.json" >/dev/null \
  || { echo "FAIL: Ask in Work did not cite the page"; exit 1; }
jq -e '.answers[0].url | test("lighthouse.html")' "$W/ask-work.json" >/dev/null || { echo "FAIL: citation has no source URL"; exit 1; }
echo "PASS: Ask in Work cites the saved page"

# 3. Ask in Personal: nothing from Work.
drive profile "$PERSONAL"
drive ask "$Q" | tee "$W/ask-personal.json"
jq -e '.answers | length == 0' "$W/ask-personal.json" >/dev/null || { echo "FAIL: Personal sees Work's capture"; exit 1; }
jq -e '.status | test("Nothing saved in Personal")' "$W/ask-personal.json" >/dev/null \
  || { echo "FAIL: Personal's empty brain is not explained"; exit 1; }
echo "PASS: Personal cannot see Work's capture, and says its brain is empty"

# 4. The app's chat knowledge search: Work finds it, Personal does not.
mc --profile Work --json profile search-knowledge "$Q" | tee "$W/chat-work.json" | jq -e 'map(select(.Path | test("inbox"))) | length > 0' >/dev/null \
  || { echo "FAIL: chat search in Work misses the capture"; exit 1; }
mc --profile Personal --json profile search-knowledge "$Q" | tee "$W/chat-personal.json" | jq -e 'map(select(.Path | test("inbox"))) | length == 0' >/dev/null \
  || { echo "FAIL: chat search in Personal sees Work's capture"; exit 1; }
echo "PASS: chat knowledge search is profile-scoped"
mono_scan_log "$W/bridge.log"
mono_check_clean
echo "artifacts: $W"
