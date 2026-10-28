#!/usr/bin/env bash
# Tests scripts/stamp-app-version.sh. Runs in CI (job release-guard-test) and locally:
#
#   bash scripts/stamp-app-version-test.sh
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
stamp() { bash "$here/stamp-app-version.sh" "$@"; }
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }
version_of() { node -e 'console.log(JSON.parse(require("fs").readFileSync(process.argv[1], "utf8")).info.productVersion)' "$1"; }

# The repo's own wails.json takes the tag, and nothing else in it changes.
cp "$here/../wails-app/wails.json" "$work/wails.json"
stamp v1.2.3 "$work/wails.json" > /dev/null || fail "stamping the repo's wails.json"
[ "$(version_of "$work/wails.json")" = "1.2.3" ] || fail "productVersion is $(version_of "$work/wails.json"), want 1.2.3"
node -e '
const fs = require("fs");
const [a, b] = process.argv.slice(1).map((f) => JSON.parse(fs.readFileSync(f, "utf8")));
delete a.info.productVersion; delete b.info.productVersion;
if (JSON.stringify(a) !== JSON.stringify(b)) process.exit(1);
' "$here/../wails-app/wails.json" "$work/wails.json" || fail "stamping changed more than productVersion"
echo "ok: a vX.Y.Z tag is written without the v"

stamp 4.5.6 "$work/wails.json" > /dev/null || fail "stamping a bare X.Y.Z"
[ "$(version_of "$work/wails.json")" = "4.5.6" ] || fail "a bare X.Y.Z was not written"
echo "ok: a bare X.Y.Z is accepted"

for bad in v1.2 v1.2.3-rc.1 latest ""; do
  cp "$work/wails.json" "$work/before.json"
  if stamp "$bad" "$work/wails.json" > /dev/null 2>&1; then fail "tag '$bad' was accepted"; fi
  cmp -s "$work/wails.json" "$work/before.json" || fail "tag '$bad' changed the file"
done
echo "ok: a tag that is not X.Y.Z is refused and leaves the file alone"

echo '{"name": "x"}' > "$work/noinfo.json"
if stamp v1.2.3 "$work/noinfo.json" > /dev/null 2>&1; then fail "a wails.json with no info block was accepted"; fi
echo "ok: a wails.json with no info block is refused"
