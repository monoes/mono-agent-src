#!/usr/bin/env bash
# Decides which CI jobs a change set needs. Reads the changed file paths
# (one per line, relative to the repository root) on stdin and prints
#   go=  wails=  extension=  nosocial=  account=  docs_only=  ci_workflow=
# as true/false lines, ready to append to $GITHUB_OUTPUT.
#
# Fail-safe by design: an empty list, a change to the workflows, this script or
# go.mod/go.sum, or any path no rule below recognises turns jobs ON, never off.
# A rule that is too narrow only lets a regression through on the pull request;
# CI on push to master runs everything (it does not call this script's trims).
#
# CI_CHANGES_ROOT (default .) is the checkout the nosocial rule inspects.
set -euo pipefail

root="${CI_CHANGES_ROOT:-.}"
all=0 go=0 wails=0 ext=0 nosocial=0 account=0 code=0 n=0

# True when a Go file in the repository names this doc file: tests and
# go:embed read documents (docs/embed.go, the account rollout tests), so a
# doc a Go file names is code for CI purposes.
doc_is_referenced() {
  local base
  base="$(basename "$1")"
  grep -rqF --include='*.go' --exclude-dir=node_modules --exclude-dir=.git -- "$base" "$root" 2>/dev/null
}

# True when the package directory holds a file built only with/without nosocial.
dir_has_nosocial_tag() {
  local d="$root/$1"
  [ -d "$d" ] || return 1
  grep -lE '//go:build.*nosocial' "$d"/*.go >/dev/null 2>&1
}

while IFS= read -r f || [ -n "$f" ]; do
  f="${f#./}"
  [ -n "$f" ] || continue
  n=$((n + 1))
  case "$f" in
    .github/workflows/* | .github/scripts/ci-changes*.sh | go.mod | go.sum | */go.mod | */go.sum)
      all=1
      continue
      ;;
    wails-app/*)
      code=1
      wails=1
      continue
      ;;
    chrome-extension/*)
      # extension_embed.go embeds the directory into the binary, so the Go
      # build and tests see it too.
      code=1
      ext=1
      go=1
      wails=1
      continue
      ;;
  esac
  case "$f" in
    *.md | docs/*)
      if [[ "$f" != *.go ]] && ! doc_is_referenced "$f"; then continue; fi
      ;;
  esac
  # Anything else is root-module code (or something we do not recognise).
  code=1
  go=1
  wails=1 # wails-app imports the root module through a replace directive
  dir="$(dirname "$f")"
  if dir_has_nosocial_tag "$dir"; then nosocial=1; fi
  case "$f" in
    internal/account/* | internal/accountsmoke/* | internal/accountdoor/* | cmd/*) account=1 ;;
    internal/storage/*migrat* | internal/storage/*lock* | internal/storage/*.sql | internal/storage/database*.go) account=1 ;;
  esac
done

docs_only=false
if [ "$n" -eq 0 ] || [ "$all" -eq 1 ]; then
  go=1 wails=1 ext=1 nosocial=1 account=1
  all=1
elif [ "$code" -eq 0 ]; then
  docs_only=true
fi

b() { if [ "$1" -eq 1 ]; then echo true; else echo false; fi; }
ci_workflow=false
[ "$all" -eq 1 ] && ci_workflow=true
echo "go=$(b $go)"
echo "wails=$(b $wails)"
echo "extension=$(b $ext)"
echo "nosocial=$(b $nosocial)"
echo "account=$(b $account)"
echo "docs_only=$docs_only"
echo "ci_workflow=$ci_workflow"
