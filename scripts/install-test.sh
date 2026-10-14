#!/usr/bin/env bash
# install-test.sh — exercises install.sh against a local python3 http.server
# serving a fake manifest and fake binary. Installs only into a throwaway
# INSTALL_DIR; never touches the real install dir, ~/.monoagent or PATH, and
# makes no network calls (the legacy GitHub path is not exercised).
#
# Run:  bash scripts/install-test.sh
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
installer="$root/install.sh"

for t in python3 openssl curl tar; do
  command -v "$t" >/dev/null 2>&1 || { echo "install-test: SKIP ($t not available)"; exit 0; }
done

case "$(uname -s)" in Darwin) os=darwin ;; Linux) os=linux ;; *) echo "install-test: SKIP (unsupported OS)"; exit 0 ;; esac
case "$(uname -m)" in arm64|aarch64) arch=arm64 ;; *) arch=amd64 ;; esac

work="$(mktemp -d "${TMPDIR:-$HOME/scratch}/install-test.XXXXXX" 2>/dev/null || mktemp -d)"
srv_pid=""
cleanup() { [ -n "$srv_pid" ] && kill "$srv_pid" 2>/dev/null || true; rm -rf "$work"; }
trap cleanup EXIT

web="$work/web"; mkdir -p "$web"
sha() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'; else shasum -a 256 "$1" | awk '{print $1}'; fi; }

port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
(cd "$web" && exec python3 -m http.server "$port" --bind 127.0.0.1 >/dev/null 2>&1) &
srv_pid=$!
base="http://127.0.0.1:$port"
for _ in $(seq 1 50); do curl -fs -o /dev/null "$base/" && break; sleep 0.1; done

# Fake release: a raw binary and a tarball holding one, and a desktop app
# tarball laid out like the real MonoAgent-linux-amd64.tar.gz.
asset="monoagentcli-$os-$arch"
printf '#!/bin/sh\necho fake-monoagentcli\n' > "$web/$asset"
mkdir -p "$work/pkg" && cp "$web/$asset" "$work/pkg/monoagentcli"
tar -czf "$web/$asset.tar.gz" -C "$work/pkg" monoagentcli
mkdir -p "$work/apppkg" && cp "$web/$asset" "$work/apppkg/monoagentcli"
printf '#!/bin/sh\necho fake-app\n' > "$work/apppkg/MonoAgent-linux-amd64"
tar -czf "$web/MonoAgent-linux-amd64.tar.gz" -C "$work/apppkg" MonoAgent-linux-amd64 monoagentcli

write_manifest() { # <asset-name> <sha> [app-sha]
  cat > "$web/manifest.json" <<EOF
{"schema":1,"version":"v9.9.9","released_at":"2026-10-08T00:00:00Z","notes_url":"$base/notes",
 "assets":[
  {"os":"plan9","arch":"amd64","name":"other","url":"$base/other","sha256":"00","size":1,"kind":"cli"},
  {"os":"$os","arch":"$arch","name":"MonoAgent-linux-amd64.tar.gz","url":"$base/MonoAgent-linux-amd64.tar.gz","sha256":"${3:-00}","size":1,"kind":"app"},
  {"os":"$os","arch":"$arch","name":"$1","url":"$base/$1","sha256":"$2","size":10,"kind":"cli"}
 ]}
EOF
}

# Test key pair, generated here; never committed.
openssl genpkey -algorithm ed25519 -out "$work/key.pem" 2>/dev/null
pub="$(openssl pkey -in "$work/key.pem" -pubout -outform DER 2>/dev/null | tail -c 32 | openssl base64 -A)"
keyid="$(openssl pkey -in "$work/key.pem" -pubout -outform DER 2>/dev/null | tail -c 32 | openssl dgst -sha256 -binary | od -An -tx1 | tr -d ' \n' | cut -c1-16)"
sign() {
  openssl pkeyutl -sign -inkey "$work/key.pem" -rawin -in "$web/manifest.json" -out "$work/sig.bin" 2>/dev/null
  printf '%s %s\n' "$keyid" "$(openssl base64 -A < "$work/sig.bin")" > "$web/manifest.json.sig"
}

fails=0
run() { # <name> <expect: ok|fail> <grep-pattern> [env...]; extra args are env assignments
  name="$1"; want="$2"; pat="$3"; shift 3
  dest="$work/prefix-$name"
  flags=()
  for a in "$@"; do [ "$a" = --app-flag ] && flags+=(--app); done
  envs=()
  for a in "$@"; do [ "$a" = --app-flag ] || envs+=("$a"); done
  set -- ${envs[@]+"${envs[@]}"}
  rc=0
  out="$(env -u MONOAGENT_RELEASE_PUBKEY HOME="$work/home-$name" INSTALL_DIR="$dest" MONOAGENT_TEST_ASSET_URL_PREFIX="$base/" MONOAGENT_MANIFEST_URL="$base/manifest.json" "$@" bash "$installer" ${flags[@]+"${flags[@]}"} 2>&1)" || rc=$?
  ok=1
  if [ "$want" = ok ]; then [ "$rc" -eq 0 ] && [ -x "$dest/monoagentcli" ] || ok=0
  else [ "$rc" -ne 0 ] && [ ! -e "$dest/monoagentcli" ] || ok=0; fi
  printf '%s\n' "$out" | grep -q -e "$pat" || ok=0
  if [ "$ok" -eq 1 ]; then echo "PASS $name"; else echo "FAIL $name (rc=$rc)"; printf '%s\n' "$out" | sed 's/^/    /'; fails=$((fails+1)); fi
}

# 1. the installer's pinned release key refuses a manifest signed by any
# other key (here the test key).
write_manifest "$asset" "$(sha "$web/$asset")"; sign
run pinned-key-refuses-other fail "not the pinned key"

# 1b. checksum-only: an openssl without Ed25519 says so and does not claim a
# signature. A stub openssl that fails everything stands in for one.
mkdir -p "$work/stub" && printf '#!/bin/sh\nexit 1\n' > "$work/stub/openssl" && chmod +x "$work/stub/openssl"
run no-ed25519-openssl ok "signature verification is SKIPPED" PATH="$work/stub:$PATH"
out="$(env -u MONOAGENT_RELEASE_PUBKEY PATH="$work/stub:$PATH" INSTALL_DIR="$work/p-neg" MONOAGENT_TEST_ASSET_URL_PREFIX="$base/" MONOAGENT_MANIFEST_URL="$base/manifest.json" bash "$installer" 2>&1)"
if printf '%s' "$out" | grep -q "signature verified"; then echo "FAIL claimed verification without checking"; fails=$((fails+1)); else echo "PASS no false claim"; fi

# 2. tarball asset.
write_manifest "$asset.tar.gz" "$(sha "$web/$asset.tar.gz")"; sign
run tarball ok "Checksum verified" MONOAGENT_RELEASE_PUBKEY="$pub"

# 3. signed with the pinned key.
write_manifest "$asset" "$(sha "$web/$asset")"; sign
run signed ok "Manifest signature verified" MONOAGENT_RELEASE_PUBKEY="$pub"

# 4. wrong sha256 in a validly signed manifest.
write_manifest "$asset" "$(printf 'x%.0s' $(seq 1 64))"; sign
run bad-sha fail "checksum mismatch" MONOAGENT_RELEASE_PUBKEY="$pub"

# 5. manifest altered after signing.
write_manifest "$asset" "$(sha "$web/$asset")"; sign
sed 's/v9.9.9/v6.6.6/' "$web/manifest.json" > "$work/m2" && cp "$work/m2" "$web/manifest.json"
run tampered fail "signature verification FAILED" MONOAGENT_RELEASE_PUBKEY="$pub"

# 6. key configured but signature missing.
write_manifest "$asset" "$(sha "$web/$asset")"; rm -f "$web/manifest.json.sig"
run missing-sig fail "signature verification FAILED" MONOAGENT_RELEASE_PUBKEY="$pub"

# 7. signed by a different key.
sign_other() { openssl genpkey -algorithm ed25519 -out "$work/o.pem" 2>/dev/null
  openssl pkeyutl -sign -inkey "$work/o.pem" -rawin -in "$web/manifest.json" -out "$work/o.bin" 2>/dev/null
  printf '%s %s\n' "$keyid" "$(openssl base64 -A < "$work/o.bin")" > "$web/manifest.json.sig"; }
sign_other
run wrong-key fail "signature verification FAILED" MONOAGENT_RELEASE_PUBKEY="$pub"

# 7b. signature carries a key id other than the pinned one.
write_manifest "$asset" "$(sha "$web/$asset")"; sign
sed 's/^[0-9a-f]*/0000000000000000/' "$web/manifest.json.sig" > "$work/s2" && cp "$work/s2" "$web/manifest.json.sig"
run wrong-keyid fail "not the pinned key" MONOAGENT_RELEASE_PUBKEY="$pub"

# 7c. malformed one-field signature file.
sign; awk '{print $2}' "$web/manifest.json.sig" > "$work/s3" && cp "$work/s3" "$web/manifest.json.sig"
run one-field-sig fail "malformed manifest.json.sig" MONOAGENT_RELEASE_PUBKEY="$pub"

# 7d. asset url on a disallowed host; the test prefix override is emptied so
# the real allow-list applies.
cat > "$web/manifest.json" <<EOF
{"schema":1,"version":"v9.9.9","assets":[{"os":"$os","arch":"$arch","name":"$asset","url":"https://evil.example.com/$asset","sha256":"00","size":1,"kind":"cli"}]}
EOF
sign
run bad-host fail "not https on an allowed host" MONOAGENT_TEST_ASSET_URL_PREFIX="" MONOAGENT_RELEASE_PUBKEY="$pub"

# 7e. validly signed but expired manifest.
write_manifest "$asset" "$(sha "$web/$asset")"
sed 's/"schema":1,/"schema":1,"expires_at":"2020-01-01T00:00:00Z",/' "$web/manifest.json" > "$work/m3" && cp "$work/m3" "$web/manifest.json"; sign
run expired fail "expired" MONOAGENT_RELEASE_PUBKEY="$pub"

# 7f. no INSTALL_DIR: ~/.local/bin unless /usr/local/bin is writable, never
# sudo; and the PATH hint when the dir is not on PATH.
write_manifest "$asset" "$(sha "$web/$asset")"; sign
h="$work/home-default"
out="$(env -u INSTALL_DIR -u MONOAGENT_RELEASE_PUBKEY HOME="$h" PATH="/usr/bin:/bin" MONOAGENT_RELEASE_PUBKEY="$pub" MONOAGENT_TEST_ASSET_URL_PREFIX="$base/" MONOAGENT_MANIFEST_URL="$base/manifest.json" bash "$installer" 2>&1)" || true
if [ -w /usr/local/bin ]; then want_dir=/usr/local/bin; else want_dir="$h/.local/bin"; fi
if [ "$want_dir" = "$h/.local/bin" ] && [ -x "$h/.local/bin/monoagentcli" ] \
   && printf '%s' "$out" | grep -q "is not on your PATH" && printf '%s' "$out" | grep -q "fish_add_path $h/.local/bin"; then
  echo "PASS default-dir-no-sudo"
elif [ "$want_dir" = /usr/local/bin ]; then
  echo "SKIP default-dir-no-sudo (/usr/local/bin is writable here)"
else
  echo "FAIL default-dir-no-sudo"; printf '%s\n' "$out" | sed 's/^/    /'; fails=$((fails+1))
fi

# 7g. --app installs the desktop app next to its CLI, with a launcher entry.
if [ "$os/$arch" = linux/amd64 ]; then
  write_manifest "$asset" "$(sha "$web/$asset")" "$(sha "$web/MonoAgent-linux-amd64.tar.gz")"; sign
  run app ok "Installed the desktop app" MONOAGENT_RELEASE_PUBKEY="$pub" --app-flag
  h="$work/home-app"
  if [ -x "$h/.local/share/monoagent/MonoAgent" ] && [ -x "$h/.local/share/monoagent/monoagentcli" ] \
     && grep -q "Exec=$h/.local/share/monoagent/MonoAgent" "$h/.local/share/applications/monoagent.desktop" \
     && [ -L "$h/.local/bin/monoagent" ]; then echo "PASS app-layout"; else echo "FAIL app-layout"; fails=$((fails+1)); fi

  # 7h. --app with a wrong app checksum installs nothing.
  write_manifest "$asset" "$(sha "$web/$asset")" "$(printf 'y%.0s' $(seq 1 64))"; sign
  run app-bad-sha fail "checksum mismatch for MonoAgent-linux-amd64.tar.gz" MONOAGENT_RELEASE_PUBKEY="$pub" --app-flag
else
  echo "SKIP app (desktop app tarball test is linux/amd64 only)"
fi

# 9. macOS --app, run on any host: a stub uname says Darwin/arm64, and PATH
# holds only the tools install.sh uses, without sha256sum (so the shasum
# branch runs, as on a Mac) and without sudo. xattr and ditto are stubs that
# log any call: install.sh needs neither (curl sets no quarantine flag, and
# the release zip is a plain `zip -r` of MonoAgent.app).
mac_ok=1
for t in zip unzip shasum perl; do command -v "$t" >/dev/null 2>&1 || mac_ok=0; done
if [ "$mac_ok" -eq 1 ]; then
  mac="$work/mac"; mkdir -p "$mac/bin" "$mac/libressl"
  for t in bash sh env curl openssl awk sed grep head tr cut od wc mkdir mv chmod find tar unzip mktemp rm date id dirname cat ln shasum perl; do
    ln -s "$(command -v "$t")" "$mac/bin/$t"
  done
  printf '#!/bin/sh\ncase "$1" in -m) echo arm64 ;; *) echo Darwin ;; esac\n' > "$mac/bin/uname"
  for t in xattr ditto; do printf '#!/bin/sh\necho "%s $*" >> "%s"\n' "$t" "$mac/calls.log" > "$mac/bin/$t"; done
  # An openssl that reads Ed25519 keys but has no pkeyutl -rawin (LibreSSL).
  printf '#!/bin/sh\nif [ "$1" = pkeyutl ]; then echo "usage: pkeyutl [-in file] [-sigfile file]"; exit 1; fi\nexec "%s" "$@"\n' "$(command -v openssl)" > "$mac/libressl/openssl"
  chmod +x "$mac/bin/uname" "$mac/bin/xattr" "$mac/bin/ditto" "$mac/libressl/openssl"

  # Fixture release: the darwin CLI and MonoAgent-darwin-arm64.zip laid out
  # like release.yml's (the app binary, the bundled CLI, Info.plist, NOTICE).
  printf '#!/bin/sh\necho fake-monoagentcli-darwin\n' > "$web/monoagentcli-darwin-arm64"
  b="$mac/pkg/MonoAgent.app/Contents"; mkdir -p "$b/MacOS" "$b/Resources"
  printf '#!/bin/sh\necho fake-app\n' > "$b/MacOS/MonoAgent"
  cp "$web/monoagentcli-darwin-arm64" "$b/MacOS/monoagentcli"
  chmod +x "$b/MacOS/MonoAgent" "$b/MacOS/monoagentcli"
  printf '<plist/>\n' > "$b/Info.plist"; printf 'notice\n' > "$b/Resources/NOTICE"
  (cd "$mac/pkg" && zip -qr "$web/MonoAgent-darwin-arm64.zip" MonoAgent.app)
  printf 'extension\n' > "$web/monoagent-chrome-extension.zip"
  # The extension entry (kind "extension", no os/arch) must be ignored.
  write_mac_manifest() { # <app-sha>
    cat > "$web/manifest.json" <<EOF
{"schema":1,"version":"v9.9.9","released_at":"2026-10-08T00:00:00Z","notes_url":"$base/notes",
 "assets":[
  {"name":"monoagent-chrome-extension.zip","url":"$base/monoagent-chrome-extension.zip","sha256":"$(sha "$web/monoagent-chrome-extension.zip")","size":10,"kind":"extension"},
  {"os":"linux","arch":"amd64","name":"MonoAgent-linux-amd64.tar.gz","url":"$base/MonoAgent-linux-amd64.tar.gz","sha256":"00","size":1,"kind":"app"},
  {"os":"darwin","arch":"arm64","name":"MonoAgent-darwin-arm64.zip","url":"$base/MonoAgent-darwin-arm64.zip","sha256":"$1","size":1,"kind":"app"},
  {"os":"darwin","arch":"arm64","name":"monoagentcli-darwin-arm64","url":"$base/monoagentcli-darwin-arm64","sha256":"$(sha "$web/monoagentcli-darwin-arm64")","size":10,"kind":"cli"}
 ]}
EOF
    sign
  }
  app_sha="$(sha "$web/MonoAgent-darwin-arm64.zip")"
  write_mac_manifest "$app_sha"
  mac_app_ok() { # <run-name> <apps-dir>: the bundle and its bundled CLI are in place
    [ -x "$2/MonoAgent.app/Contents/MacOS/MonoAgent" ] && [ -x "$2/MonoAgent.app/Contents/MacOS/monoagentcli" ] \
      && [ -f "$2/MonoAgent.app/Contents/Info.plist" ] && [ -f "$2/MonoAgent.app/Contents/Resources/NOTICE" ] \
      && "$work/prefix-$1/monoagentcli" | grep -q fake-monoagentcli-darwin
  }
  check() { # <name> <status>
    if [ "$2" -eq 0 ]; then echo "PASS $1"; else echo "FAIL $1"; fails=$((fails+1)); fi
  }

  # 9a. /Applications writable: the app goes there, ~/Applications is untouched.
  sysapps="$work/Applications-rw"; mkdir -p "$sysapps"
  run mac-app-system ok "Installed the desktop app: $sysapps/MonoAgent.app" \
    PATH="$mac/bin" MONOAGENT_TEST_APPLICATIONS_DIR="$sysapps" MONOAGENT_RELEASE_PUBKEY="$pub" --app-flag
  st=0
  { printf '%s\n' "$out" | grep -q "Manifest signature verified" \
    && printf '%s\n' "$out" | grep -q "Checksum verified: MonoAgent-darwin-arm64.zip SHA256 $app_sha" \
    && printf '%s\n' "$out" | grep -q "(v9.9.9, darwin/arm64)" \
    && mac_app_ok mac-app-system "$sysapps" && [ ! -e "$work/home-mac-app-system/Applications" ]; } || st=1
  check mac-app-system-layout "$st"

  # 9b. /Applications not writable: ~/Applications. (Root can write anything,
  # so this needs a non-root run.)
  if [ "$(id -u)" != 0 ]; then
    roapps="$work/Applications-ro"; mkdir -p "$roapps"; chmod 555 "$roapps"
    run mac-app-user ok "Installed the desktop app: $work/home-mac-app-user/Applications/MonoAgent.app" \
      PATH="$mac/bin" MONOAGENT_TEST_APPLICATIONS_DIR="$roapps" MONOAGENT_RELEASE_PUBKEY="$pub" --app-flag
    st=0
    { mac_app_ok mac-app-user "$work/home-mac-app-user/Applications" && [ -z "$(ls -A "$roapps")" ]; } || st=1
    check mac-app-user-layout "$st"
    chmod 755 "$roapps"
  else
    echo "SKIP mac-app-user (running as root)"
  fi

  # 9c. a second install replaces the bundle; nothing of the old one is left.
  mkdir -p "$sysapps/MonoAgent.app/Contents/MacOS" && echo old > "$sysapps/MonoAgent.app/Contents/MacOS/stale"
  run mac-app-replace ok "Installed the desktop app: $sysapps/MonoAgent.app" \
    PATH="$mac/bin" MONOAGENT_TEST_APPLICATIONS_DIR="$sysapps" MONOAGENT_RELEASE_PUBKEY="$pub" --app-flag
  st=0
  { mac_app_ok mac-app-replace "$sysapps" && [ ! -e "$sysapps/MonoAgent.app/Contents/MacOS/stale" ]; } || st=1
  check mac-app-replace-layout "$st"

  # 9d. a zip that does not match the signed manifest installs nothing.
  cp "$web/MonoAgent-darwin-arm64.zip" "$work/good.zip"
  printf 'tampered' >> "$web/MonoAgent-darwin-arm64.zip"
  tapps="$work/Applications-tamper"; mkdir -p "$tapps"
  run mac-app-tampered fail "checksum mismatch for MonoAgent-darwin-arm64.zip" \
    PATH="$mac/bin" MONOAGENT_TEST_APPLICATIONS_DIR="$tapps" MONOAGENT_RELEASE_PUBKEY="$pub" --app-flag
  st=0
  { [ -z "$(ls -A "$tapps")" ] && [ ! -e "$work/home-mac-app-tampered/Applications" ]; } || st=1
  check mac-app-tampered-nothing-installed "$st"
  cp "$work/good.zip" "$web/MonoAgent-darwin-arm64.zip"

  # 9e. an openssl without pkeyutl -rawin (LibreSSL) is checksum-only and
  # says so, rather than failing every signature.
  run mac-libressl ok "cannot verify raw Ed25519 signatures" \
    PATH="$mac/libressl:$mac/bin" MONOAGENT_TEST_APPLICATIONS_DIR="$sysapps" MONOAGENT_RELEASE_PUBKEY="$pub" --app-flag
  st=0
  { printf '%s\n' "$out" | grep -q "Checksum verified: MonoAgent-darwin-arm64.zip" \
    && ! printf '%s\n' "$out" | grep -q "signature verified"; } || st=1
  check mac-libressl-checksum-only "$st"

  # 9f. no run called xattr or ditto.
  st=0; [ ! -s "$mac/calls.log" ] || st=1
  check mac-no-xattr-ditto "$st"
else
  echo "SKIP mac-app (needs zip, unzip, shasum and perl)"
fi

# 8. no asset for this platform.
printf '{"schema":1,"version":"v9.9.9","assets":[]}' > "$web/manifest.json"; sign
run no-asset fail "no CLI asset" MONOAGENT_RELEASE_PUBKEY="$pub"

if [ "$fails" -ne 0 ]; then echo "install-test: $fails failure(s)"; exit 1; fi
echo "install-test: all passed"
