#!/usr/bin/env bash
# stamp-app-version.sh — write a release tag into wails.json's info.productVersion
# before `wails build`, so the desktop app's own version (macOS Info.plist
# CFBundleShortVersionString, the Windows file version) is the release's, not
# whatever number was last committed to wails.json.
#
#   bash scripts/stamp-app-version.sh v0.176.2 wails-app/wails.json
#
# The tag must be vX.Y.Z (or X.Y.Z): Windows version resources take only
# numbers. Uses node, which every app build job already sets up.
set -euo pipefail

tag="${1:?usage: stamp-app-version.sh <tag> <wails.json>}"
file="${2:?usage: stamp-app-version.sh <tag> <wails.json>}"
version="${tag#v}"
if ! [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "stamp-app-version: $tag is not vX.Y.Z" >&2
  exit 1
fi

node -e '
const fs = require("fs");
const [file, version] = process.argv.slice(1);
const cfg = JSON.parse(fs.readFileSync(file, "utf8"));
if (!cfg.info || typeof cfg.info !== "object") {
  console.error(`stamp-app-version: ${file} has no "info" block`);
  process.exit(1);
}
cfg.info.productVersion = version;
fs.writeFileSync(file, JSON.stringify(cfg, null, 2) + "\n");
' "$file" "$version"
echo "stamped $file: productVersion $version"
