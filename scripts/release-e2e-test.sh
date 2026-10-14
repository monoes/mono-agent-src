#!/usr/bin/env bash
# release-e2e-test.sh — end-to-end check of the signed release manifest:
#   cmd/release-manifest (manifest, sign)  ->  monoagentcli release verify
#   ->  install.sh  ->  monoagentcli update
# against ONE loopback python3 http.server. No real network, no keyring, no
# real ~/.monoagent: everything (HOME, install dirs, keys, binaries) lives in a
# fresh directory under $E2E_ROOT (default ~/scratch/e2e).
#
# The Go binaries are built with -tags releasee2e (never a release build):
#   MONOAGENT_E2E_PINNED_KEY  pins a public key line in the binary
#   MONOAGENT_E2E_LOOPBACK    sends the update client's requests to host:port
# The client still checks the real https URLs and host allow-list.
#
# Run:  bash scripts/release-e2e-test.sh      (needs go, python3, openssl, curl, tar)
set -uo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
for t in go python3 openssl curl tar; do
  command -v "$t" >/dev/null 2>&1 || { echo "release-e2e: SKIP ($t not available)"; exit 0; }
done
goos="$(go env GOOS)"; goarch="$(go env GOARCH)"
case "$goos/$goarch" in
  linux/amd64|linux/arm64|darwin/amd64|darwin/arm64) ;;
  *) echo "release-e2e: SKIP ($goos/$goarch is not an install.sh platform)"; exit 0 ;;
esac

scratch="${E2E_ROOT:-$HOME/scratch/e2e}"
mkdir -p "$scratch"
work="$(mktemp -d "$scratch/run.XXXXXX")"
srv_pid=""
cleanup() {
  [ -n "$srv_pid" ] && kill "$srv_pid" 2>/dev/null || true
  [ -n "${E2E_KEEP:-}" ] || find "$work" -delete 2>/dev/null || true
}
trap cleanup EXIT

home="$work/home"; mkdir -p "$home"
srv="$work/srv"; mkdir -p "$srv"
bin="$work/bin"; mkdir -p "$bin"
fails=0; passes=0
pass() { passes=$((passes+1)); echo "PASS $1"; }
fail() { fails=$((fails+1)); echo "FAIL $1"; [ -z "${2:-}" ] || printf '%s\n' "$2" | sed 's/^/    /'; }
check() { # <name> <exit-status-of-condition> [output]
  if [ "$2" -eq 0 ]; then pass "$1"; else fail "$1" "${3:-}"; fi
}

# ── Build (real HOME for the Go cache; test HOME only when running binaries) ─
echo "== build"
build_cli() { # <version> <out>
  (cd "$root" && go build -tags releasee2e -ldflags "-X main.version=$1" -o "$2" ./cmd/monoagentcli)
}
build_cli v0.1.0 "$bin/old-cli"
build_cli v9.9.9 "$bin/new-cli"
(cd "$root" && go build -o "$bin/release-manifest" ./cmd/release-manifest)
rm_tool="$bin/release-manifest"

# ── Keys (Ed25519 via openssl; the file format is what `sign -key-file` reads) ─
mkkey() { # <name> -> $work/<name>.key (base64 seed||pub), <name>.line ("<id> <b64 pub>")
  openssl genpkey -algorithm ed25519 -out "$work/$1.pem" 2>/dev/null
  openssl pkey -in "$work/$1.pem" -outform DER 2>/dev/null | tail -c 32 > "$work/$1.seed"
  openssl pkey -in "$work/$1.pem" -pubout -outform DER 2>/dev/null | tail -c 32 > "$work/$1.pub"
  cat "$work/$1.seed" "$work/$1.pub" | openssl base64 -A > "$work/$1.key"
  chmod 600 "$work/$1.key" # release-manifest refuses a signing key readable by others
  local id; id="$(openssl dgst -sha256 -binary < "$work/$1.pub" | od -An -tx1 | tr -d ' \n' | cut -c1-16)"
  printf '%s %s' "$id" "$(openssl base64 -A < "$work/$1.pub")" > "$work/$1.line"
  openssl base64 -A < "$work/$1.pub" > "$work/$1.pub64"
}
mkkey good; mkkey other
good_line="$(cat "$work/good.line")"; other_line="$(cat "$work/other.line")"
good_pub64="$(cat "$work/good.pub64")"; other_pub64="$(cat "$work/other.pub64")"

# ── Fake artifacts named exactly as release.yml names them ───────────────────
make_artifacts() { # <dir> <version> : the host CLI asset is a real binary for v9.9.9
  local d="$1" v="$2"
  mkdir -p "$d"
  for o in linux darwin; do for a in amd64 arm64; do printf 'cli %s %s %s\n' "$o" "$a" "$v" > "$d/monoagentcli-$o-$a"; done; done
  printf 'cli windows amd64 %s\n' "$v" > "$d/monoagentcli-windows-amd64.exe"
  printf 'app darwin arm64 %s\n' "$v" > "$d/MonoAgent-darwin-arm64.zip"
  printf 'app windows amd64 %s\n' "$v" > "$d/MonoAgent-windows-amd64.exe"
  printf 'bundled windows amd64 %s\n' "$v" > "$d/monoagentcli-windows-amd64-bundled.exe"
  printf 'app linux amd64 %s\n' "$v" > "$d/MonoAgent-linux-amd64.tar.gz"
  printf 'extension %s\n' "$v" > "$d/monoagent-chrome-extension.zip"   # in SHA256SUMS and the manifest
  if [ "$v" = v9.9.9 ]; then cp "$bin/new-cli" "$d/monoagentcli-$goos-$goarch"
  else printf '#!/bin/sh\necho "monoagentcli %s (fake)"\n' "$v" > "$d/monoagentcli-$goos-$goarch"; fi
}

echo "== 1-3: keys, artifacts, manifest, sign"
for v in v9.9.9 v9.0.0; do make_artifacts "$work/art-$v" "$v"; done
"$rm_tool" manifest -dir "$work/art-v9.9.9" -version v9.9.9 >/dev/null
"$rm_tool" sign -manifest "$work/art-v9.9.9/manifest.json" -key-file "$work/good.key" > "$work/sign.out"
grep -q "key ${good_line%% *}\$" "$work/sign.out"; check "sign reports the key id of the generated key" $? "$(cat "$work/sign.out")"

py() { python3 - "$@"; }
py "$work/art-v9.9.9/manifest.json" <<'EOF'
import json, sys
m = json.load(open(sys.argv[1]))
names = sorted(a["name"] for a in m["assets"])
want = sorted(["monoagentcli-linux-amd64","monoagentcli-linux-arm64","monoagentcli-darwin-amd64","monoagentcli-darwin-arm64",
  "monoagentcli-windows-amd64.exe","MonoAgent-darwin-arm64.zip","MonoAgent-windows-amd64.exe",
  "monoagentcli-windows-amd64-bundled.exe","MonoAgent-linux-amd64.tar.gz","monoagent-chrome-extension.zip"])
assert names == want, names
assert "expires_at" not in m
assert all(a["url"].startswith("https://github.com/monoes/mono-agent/releases/download/v9.9.9/") for a in m["assets"])
EOF
check "manifest lists the CLI+app assets and the extension zip, no expires_at by default" $?
grep -q "monoagent-chrome-extension.zip" "$work/art-v9.9.9/SHA256SUMS"; check "SHA256SUMS covers the extension zip" $?
! grep -q "manifest.json" "$work/art-v9.9.9/SHA256SUMS"; check "SHA256SUMS excludes manifest files" $?
(cd "$work/art-v9.9.9" && sha256sum -c SHA256SUMS >/dev/null 2>&1 || shasum -a 256 -c SHA256SUMS >/dev/null 2>&1); check "SHA256SUMS verifies against the artifacts" $?

# with -expires-days (separate dir copy so the default manifest stays)
mkdir -p "$work/exp"; cp "$work/art-v9.9.9/"* "$work/exp/"; find "$work/exp" -name 'manifest.json*' -delete
"$rm_tool" manifest -dir "$work/exp" -version v9.9.9 -expires-days 30 >/dev/null
"$rm_tool" sign -manifest "$work/exp/manifest.json" -key-file "$work/good.key" >/dev/null
py "$work/exp/manifest.json" <<'EOF'
import json, sys, datetime
m = json.load(open(sys.argv[1]))
e = datetime.datetime.strptime(m["expires_at"], "%Y-%m-%dT%H:%M:%SZ")
d = (e - datetime.datetime.now(datetime.timezone.utc).replace(tzinfo=None)).days
assert 28 <= d <= 30, d
EOF
check "-expires-days 30 writes expires_at about 30 days out" $?

# older, validly signed release (replay target)
"$rm_tool" manifest -dir "$work/art-v9.0.0" -version v9.0.0 >/dev/null
"$rm_tool" sign -manifest "$work/art-v9.0.0/manifest.json" -key-file "$work/good.key" >/dev/null

# ── (4) release verify ───────────────────────────────────────────────────────
echo "== 4: monoagentcli release verify"
cli() { HOME="$home" env -u MONOAGENT_E2E_LOOPBACK "$@"; }
m="$work/art-v9.9.9/manifest.json"; s="$m.sig"
out="$(cli "$bin/old-cli" release verify "$m" "$s" --pubkey "$good_line" 2>&1)"; rc=$?
grep -q "^OK: v9.9.9, 10 assets" <<<"$out"; check "verify --pubkey: good signature" $? "$out"
out="$(MONOAGENT_E2E_PINNED_KEY="$good_line" cli "$bin/old-cli" release verify "$m" "$s" 2>&1)"
grep -q "^OK: v9.9.9" <<<"$out"; check "verify with the key pinned in the binary" $? "$out"
out="$(cli "$bin/old-cli" release verify "$m" "$s" 2>&1)" && rc=0 || rc=$?
[ "$rc" -ne 0 ]; check "verify with no pinned key and no --pubkey is refused" $? "$out"
cli "$bin/old-cli" release verify "$m" "$s" --pubkey "$other_line" >/dev/null 2>&1 && rc=0 || rc=$?
[ "$rc" -ne 0 ]; check "verify refuses the wrong key" $?
sed 's/v9.9.9/v9.9.8/' "$m" > "$work/t.json"
cli "$bin/old-cli" release verify "$work/t.json" "$s" --pubkey "$good_line" >/dev/null 2>&1 && rc=0 || rc=$?
[ "$rc" -ne 0 ]; check "verify refuses a tampered manifest" $?
# expired: edit expires_at into the past and re-sign (what a stale manifest looks like)
sed 's/"expires_at": "[^"]*"/"expires_at": "2020-01-01T00:00:00Z"/' "$work/exp/manifest.json" > "$work/expired.json"
"$rm_tool" sign -manifest "$work/expired.json" -key-file "$work/good.key" >/dev/null
out="$(cli "$bin/old-cli" release verify "$work/expired.json" "$work/expired.json.sig" --pubkey "$good_line" 2>&1)" && rc=0 || rc=$?
[ "$rc" -ne 0 ]; check "verify refuses an expired manifest" $? "$out"
(cd "$root" && go test -count=1 -run 'TestReleaseKeygenAndVerify' ./cmd/monoagentcli/ >/dev/null 2>&1); check "release keygen (mock keyring Go test) + verify round-trip" $?

# ── serve ────────────────────────────────────────────────────────────────────
port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
python3 -m http.server "$port" --bind 127.0.0.1 --directory "$srv" >/dev/null 2>&1 &
srv_pid=$!
echo "$srv_pid" > "$work/srv.pid"
base="http://127.0.0.1:$port"
for _ in $(seq 1 50); do curl -fs -o /dev/null "$base/" && break; sleep 0.1; done

dl="monoes/mono-agent/releases/download"
publish_assets() { # <version>: assets where the manifest URLs point
  mkdir -p "$srv/$dl/$1"; cp "$work/art-$1/"* "$srv/$dl/$1/"
}
set_go_latest() { # <manifest> <sig> : monoes.me/mono-agent/releases/latest/manifest.json(.sig)
  mkdir -p "$srv/mono-agent/releases/latest"
  cp "$1" "$srv/mono-agent/releases/latest/manifest.json"; cp "$2" "$srv/mono-agent/releases/latest/manifest.json.sig"
}
set_sh_latest() { # <manifest> <keyname> : URLs rewritten to the loopback server, then signed
  mkdir -p "$srv/sh" "$work/shtmp"
  sed "s#https://github.com/$dl/#$base/$dl/#g" "$1" > "$work/shtmp/manifest.json"
  "$rm_tool" sign -manifest "$work/shtmp/manifest.json" -key-file "$work/$2.key" >/dev/null
  cp "$work/shtmp/manifest.json" "$work/shtmp/manifest.json.sig" "$srv/sh/"
}
publish_assets v9.9.9; publish_assets v9.0.0

# ── (5) install.sh ───────────────────────────────────────────────────────────
echo "== 5: install.sh"
n=0
inst() { # <name> <ok|fail> <pattern> [env...]
  local name="$1" want="$2" pat="$3"; shift 3
  local script="$root/install.sh"
  if [ "${1:-}" = --unpinned ]; then script="$work/install-unpinned.sh"; shift; fi
  n=$((n+1)); local dest="$work/inst-$n" rc=0 o
  o="$(env -u MONOAGENT_RELEASE_PUBKEY HOME="$home" INSTALL_DIR="$dest" MONOAGENT_TEST_ASSET_URL_PREFIX="$base/" \
       MONOAGENT_MANIFEST_URL="$base/sh/manifest.json" "$@" bash "$script" 2>&1)" || rc=$?
  local ok=0
  if [ "$want" = ok ]; then
    [ "$rc" -eq 0 ] && [ -x "$dest/monoagentcli" ] && [ "$("$dest/monoagentcli" version 2>&1 | head -n1 | grep -c 'v9.9.9')" -ge 1 ] && ok=1
  else
    [ "$rc" -ne 0 ] && [ ! -e "$dest/monoagentcli" ] && ok=1
  fi
  grep -q -e "$pat" <<<"$o" || ok=0
  case "$name" in *no-false-claim*) ! grep -qi "signature verified" <<<"$o" || ok=0 ;; esac
  if [ "$ok" -eq 1 ]; then pass "install: $name"; else fail "install: $name (rc=$rc)" "$o"; fi
}
# install.sh pins the release key; the "no key configured" cases run a copy
# with the pin blanked, as a build without a key would ship.
sed 's/^RELEASE_PUBKEY=".*"$/RELEASE_PUBKEY=""/' "$root/install.sh" > "$work/install-unpinned.sh"
reset_sh() { set_sh_latest "$work/art-v9.9.9/manifest.json" good; publish_assets v9.9.9; }

reset_sh
inst "signed with the pinned key: verified, right binary" ok "Manifest signature verified" MONOAGENT_RELEASE_PUBKEY="$good_pub64"
inst "no key configured: says SKIPPED, never claims a signature (no-false-claim)" ok "signature verification is SKIPPED" --unpinned
inst "wrong pinned key" fail "signature verification FAILED" MONOAGENT_RELEASE_PUBKEY="$other_pub64"

set_sh_latest "$work/exp/manifest.json" good
inst "manifest with a future expires_at installs" ok "Manifest signature verified" MONOAGENT_RELEASE_PUBKEY="$good_pub64"
set_sh_latest "$work/expired.json" good
inst "expired manifest (validly signed)" fail "expired" MONOAGENT_RELEASE_PUBKEY="$good_pub64"
inst "expired manifest, no key configured" fail "expired" --unpinned

reset_sh
sed 's/v9.9.9/v9.9.8/' "$srv/sh/manifest.json" > "$work/t2" && cp "$work/t2" "$srv/sh/manifest.json"
inst "tampered manifest" fail "signature verification FAILED" MONOAGENT_RELEASE_PUBKEY="$good_pub64"

reset_sh
printf 'x' >> "$srv/$dl/v9.9.9/monoagentcli-$goos-$goarch"
inst "tampered asset" fail "checksum mismatch" MONOAGENT_RELEASE_PUBKEY="$good_pub64"
publish_assets v9.9.9

reset_sh
awk '{ sub(/.$/, "A", $2); print }' "$srv/sh/manifest.json.sig" > "$work/t3" && cp "$work/t3" "$srv/sh/manifest.json.sig"
inst "tampered signature" fail "signature verification FAILED" MONOAGENT_RELEASE_PUBKEY="$good_pub64"

reset_sh
find "$srv/sh" -name manifest.json.sig -delete
inst "missing signature with a key configured" fail "signature verification FAILED" MONOAGENT_RELEASE_PUBKEY="$good_pub64"

# ── (6) update (the Go client) ───────────────────────────────────────────────
echo "== 6: monoagentcli update"
upd() { # runs the installed binary with the loopback + pinned key; extra args = update flags
  HOME="$home" MONOAGENT_E2E_LOOPBACK="127.0.0.1:$port" MONOAGENT_E2E_PINNED_KEY="${PIN:-$good_line}" "$work/up/monoagentcli" update "$@" 2>&1
}
ver() { "$work/up/monoagentcli" version 2>&1 | head -n1; }
fresh_old() { mkdir -p "$work/up"; find "$work/up" -name monoagentcli -delete; cp "$bin/old-cli" "$work/up/monoagentcli"; }

set_go_latest "$work/art-v9.9.9/manifest.json" "$work/art-v9.9.9/manifest.json.sig"
fresh_old
out="$(upd)" && rc=0 || rc=$?
{ [ "$rc" -eq 0 ] && grep -q "Signature and checksum verified" <<<"$out" && grep -q "Updated to v9.9.9" <<<"$out" && ver | grep -q v9.9.9; }
check "update: v0.1.0 -> v9.9.9 through the signed manifest" $? "$out"
out="$(upd)" || true
grep -q "Already on latest version (v9.9.9)" <<<"$out"; check "update: second run is a no-op" $? "$out"

# replay: an older but validly signed manifest, after v9.9.9 was accepted
set_go_latest "$work/art-v9.0.0/manifest.json" "$work/art-v9.0.0/manifest.json.sig"
fresh_old
out="$(upd)" && rc=0 || rc=$?
{ [ "$rc" -ne 0 ] && grep -q "refusing a rollback" <<<"$out" && ver | grep -q v0.1.0; }
check "update: replayed older manifest refused, binary kept" $? "$out"
out="$(upd --force)" && rc=0 || rc=$?
{ [ "$rc" -eq 0 ] && grep -q "Updated to v9.0.0" <<<"$out" && "$work/up/monoagentcli" | grep -q "fake"; }
check "update --force overrides the replay refusal" $? "$out"

# fresh HOME state for the failure cases, so each is judged on its own
bad() { # <name> <pattern> : expects failure, binary untouched
  fresh_old; rm_state; local o rc=0; o="$(upd)" || rc=$?
  { [ "$rc" -ne 0 ] && grep -q -e "$2" <<<"$o" && ver | grep -q v0.1.0; }; check "update: $1" $? "$o"
}
rm_state() { find "$home/.monoagent" -name release-state.json -delete 2>/dev/null || true; }
set_go_latest "$work/art-v9.9.9/manifest.json" "$work/art-v9.9.9/manifest.json.sig"
PIN="$other_line" bad "wrong pinned key" "rejected"
PIN="$good_line"
sed 's/v9.9.9/v9.9.8/' "$work/art-v9.9.9/manifest.json" > "$work/t4"; set_go_latest "$work/t4" "$work/art-v9.9.9/manifest.json.sig"
bad "tampered manifest" "rejected"
set_go_latest "$work/art-v9.9.9/manifest.json" "$work/art-v9.9.9/manifest.json.sig"
awk '{ sub(/.$/, "A", $2); print }' "$work/art-v9.9.9/manifest.json.sig" > "$work/t5"; set_go_latest "$work/art-v9.9.9/manifest.json" "$work/t5"
bad "tampered signature" "rejected"
set_go_latest "$work/expired.json" "$work/expired.json.sig"
bad "expired manifest" "expired"
set_go_latest "$work/art-v9.9.9/manifest.json" "$work/art-v9.9.9/manifest.json.sig"
printf 'x' >> "$srv/$dl/v9.9.9/monoagentcli-$goos-$goarch"
bad "tampered asset" "mismatch\|bytes"
publish_assets v9.9.9

echo
echo "release-e2e: $passes passed, $fails failed"
[ "$fails" -eq 0 ]
