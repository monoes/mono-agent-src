#!/usr/bin/env bash
# Build the zip for the Edge Add-ons / Chrome Web Store upload.
#
# Unlike the release asset (monoagent-chrome-extension.zip, which nests
# everything under chrome-extension/), the stores need manifest.json at the
# zip root, and they review every file: tests, harnesses and fixtures are left
# out. Usage: scripts/package-extension-store.sh [out.zip]
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out="${1:-$root/dist/monoagent-extension-store.zip}"
case "$out" in /*) ;; *) out="$PWD/$out" ;; esac

mkdir -p "$(dirname "$out")"
rm -f "$out"

cd "$root/chrome-extension"
zip -qr "$out" . \
  -x '*.DS_Store' \
  -x '*.test.mjs' '*.browser.test.mjs' \
  -x '*_harness.mjs' '*.fixtures.mjs' 'recorder_fake_dom.mjs' 'recorder_go_types.mjs' 'test_helpers.mjs' \
  -x 'fixtures/*' 'adapters/fixtures/*' 'icons/ICONS.txt'

# Every file the extension loads must be in the zip, and nothing test-only may be.
listing="$(unzip -Z1 "$out")"
grep -qx 'manifest.json' <<<"$listing" || { echo "manifest.json is not at the zip root" >&2; exit 1; }

missing=0
refs="$(
  { sed -n 's/.*importScripts(\(.*\));.*/\1/p' background.js | grep -o '"[^"]*\.js"' | tr -d '"'
    grep -o '<script src="[^"]*"' sidepanel.html | sed 's/.*src="//; s/"$//'
    grep -o '"[A-Za-z_/]*\.\(js\|html\|png\)"' manifest.json | tr -d '"'
  } | sort -u
)"
for f in $refs; do
  grep -qx "$f" <<<"$listing" || { echo "referenced but not packaged: $f" >&2; missing=1; }
done
[ "$missing" -eq 0 ] || exit 1

if grep -E '(\.test\.mjs|_harness\.mjs|\.fixtures\.mjs)$' <<<"$listing" >/dev/null; then
  echo "test-only files leaked into the package" >&2
  exit 1
fi

echo "wrote $out ($(wc -l <<<"$listing") files, $(du -h "$out" | cut -f1))"
