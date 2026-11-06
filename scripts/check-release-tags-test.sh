#!/usr/bin/env bash
# Tests scripts/check-release-tags.sh against real Go binaries built with and without the
# devaccount tag, loose and inside the archives the release ships (a .tar.gz like the Linux
# app's, a .zip like the macOS app's). Runs in CI (job release-guard-test) and locally:
#
#   bash scripts/check-release-tags-test.sh
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
guard() { bash "$here/check-release-tags.sh" "$@"; }
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

# A tiny module, built several ways. The tag changes nothing in the program: the guard reads the
# build settings the toolchain stamps into every binary.
mkdir "$work/mod"
printf 'module example.com/guardtest\n\ngo 1.26.0\n' > "$work/mod/go.mod"
printf 'package main\n\nfunc main() {}\n' > "$work/mod/main.go"
build() { (cd "$work/mod" && go build "$@" ) || fail "go build $*"; }
mkdir "$work/bin"
build -o "$work/bin/plain" .
build -tags devaccount -o "$work/bin/tagged" .
build -tags desktop,production,devaccount -o "$work/bin/tagged-among-others" .
build -tags desktop,production -o "$work/bin/other-tags" .
build -tags releasee2e -o "$work/bin/e2e-tagged" .
build -tags e2estub -o "$work/bin/stub-tagged" .
build -tags e2ehost -o "$work/bin/host-tagged" .
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 build -tags devaccount -o "$work/bin/tagged.exe" .

# expect_ok / expect_fail run the guard and check its exit status (and, for a failure, that it
# names the offending file).
expect_ok() {
  local name="$1"; shift
  guard "$@" > "$work/out" 2>&1 || { cat "$work/out"; fail "$name: the guard failed"; }
  echo "ok: $name"
}
expect_fail() {
  local name="$1" culprit="$2"; shift 2
  if guard "$@" > "$work/out" 2>&1; then cat "$work/out"; fail "$name: the guard passed a tagged binary"; fi
  grep -q "$culprit" "$work/out" || { cat "$work/out"; fail "$name: the failure should name $culprit"; }
  echo "ok: $name"
}

expect_ok "an untagged binary" "$work/bin/plain"
expect_ok "other tags are fine" "$work/bin/other-tags"
expect_fail "a tagged binary" tagged "$work/bin/tagged"
expect_fail "the tag among others" tagged-among-others "$work/bin/tagged-among-others"
expect_fail "a tagged Windows binary" tagged.exe "$work/bin/tagged.exe"
expect_fail "the release e2e test tag" e2e-tagged "$work/bin/e2e-tagged"
expect_fail "the stub-runtime e2e tag" stub-tagged "$work/bin/stub-tagged"
expect_fail "the browser e2e host tag" host-tagged "$work/bin/host-tagged"

# A folder: one tagged binary among clean ones, and a file that is not a binary.
mkdir "$work/flat"
cp "$work/bin/plain" "$work/flat/monoagentcli-linux-amd64"
printf 'not a binary\n' > "$work/flat/SHA256SUMS.txt"
expect_ok "a folder of clean binaries" "$work/flat"
cp "$work/bin/tagged" "$work/flat/monoagentcli-darwin-arm64"
expect_fail "a folder with one tagged binary" monoagentcli-darwin-arm64 "$work/flat"

# The archives of the desktop app: the app's executable and the CLI beside it.
mkdir -p "$work/linux-pkg" "$work/MonoAgent.app/Contents/MacOS"
cp "$work/bin/plain" "$work/linux-pkg/MonoAgent-linux-amd64"
cp "$work/bin/plain" "$work/linux-pkg/monoagentcli"
tar -czf "$work/clean.tar.gz" -C "$work/linux-pkg" MonoAgent-linux-amd64 monoagentcli
expect_ok "a clean tarball" "$work/clean.tar.gz"
cp "$work/bin/tagged" "$work/linux-pkg/monoagentcli"
tar -czf "$work/sidecar.tar.gz" -C "$work/linux-pkg" MonoAgent-linux-amd64 monoagentcli
expect_fail "a tagged CLI sidecar in a tarball" "sidecar.tar.gz:monoagentcli" "$work/sidecar.tar.gz"
cp "$work/bin/tagged" "$work/MonoAgent.app/Contents/MacOS/monoagent-ui"
cp "$work/bin/plain" "$work/MonoAgent.app/Contents/MacOS/monoagentcli"
(cd "$work" && zip -qr app.zip MonoAgent.app)
expect_fail "a tagged app executable in a zip" "app.zip:MonoAgent.app/Contents/MacOS/monoagent-ui" "$work/app.zip"

# --min: a layout that changed must not pass by checking nothing.
expect_ok "enough binaries" --min 2 "$work/clean.tar.gz"
if guard --min 3 "$work/clean.tar.gz" > "$work/out" 2>&1; then fail "--min 3 passed with two binaries"; fi
grep -q "expected at least 3" "$work/out" || fail "--min should say how many it expected"
mkdir "$work/empty"
if guard "$work/empty" > "$work/out" 2>&1; then fail "a folder with no binary passed"; fi
echo "ok: --min"

# A file that looks like an executable but carries no Go build info is an error, unless allowed.
mkdir "$work/nongo"
printf '\x7fELF\x02\x01\x01\x00 not really go' > "$work/nongo/fakeelf"
printf 'MZ\x90\x00 not really go' > "$work/nongo/fake.exe"
printf '\xcf\xfa\xed\xfe not really go' > "$work/nongo/fakemacho"
cp "$work/bin/plain" "$work/nongo/gobin"
expect_fail "a non-Go ELF" "fakeelf looks like an executable" "$work/nongo"
expect_fail "a non-Go PE" "fake.exe looks like an executable" "$work/nongo"
expect_fail "a non-Go Mach-O" "fakemacho looks like an executable" "$work/nongo"
expect_ok "allow-listed non-Go executables" --allow-non-go fakeelf --allow-non-go fake.exe --allow-non-go fakemacho "$work/nongo"
expect_fail "only the allow-listed one passes" "fake.exe looks like an executable" --allow-non-go fakeelf "$work/nongo"
mkdir "$work/text"; printf 'hello\n' > "$work/text/README"; cp "$work/bin/plain" "$work/text/cli"
expect_ok "plain text files stay ignored" "$work/text"

echo "check-release-tags test: ok"
