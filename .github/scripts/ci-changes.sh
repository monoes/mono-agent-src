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
# nosocial is the inverse: the nosocial tests run for any Go-relevant change
# except one to a file built only without the tag (shared packages such as
# internal/config are compiled in the nosocial build too).
#
# CI_CHANGES_ROOT (default .) is the checkout the nosocial rule inspects.
set -euo pipefail

root="${CI_CHANGES_ROOT:-.}"
all=0 go=0 wails=0 ext=0 nosocial=0 account=0 code=0 n=0

# True when a Go file in the repository names this doc file: tests read
# documents (the account rollout tests), so a doc a Go file names is code for CI
# purposes.
doc_is_referenced() {
  local base
  base="$(basename "$1")"
  grep -rqF --include='*.go' --exclude-dir=node_modules --exclude-dir=.git -- "$base" "$root" 2>/dev/null
}

# The paths the binary embeds, derived from every //go:embed line in the
# repository's Go files, one per line, relative to the repository root. A
# directory pattern embeds the whole tree (so a NEW file in it is in the binary
# without any Go file naming it); a glob pattern yields its literal directory
# prefix (conservative: everything under that directory counts); an "all:"
# prefix is dropped. An empty result line means the repository root itself.
embed_roots_cache=""
embed_roots_ready=0
embed_roots() {
  if [ "$embed_roots_ready" -eq 0 ]; then
    embed_roots_ready=1
    local line file text rel dir tok pat prefix comp out
    while IFS= read -r line; do
      file="${line%%:*}"
      text="${line#*:}"
      text="${text#"${text%%//go:embed*}"}"
      text="${text#//go:embed}"
      rel="${file#"$root"/}"
      dir="$(dirname "$rel")"
      [ "$dir" = . ] && dir=""
      read -ra toks <<<"$text"
      for tok in "${toks[@]}"; do
        pat="${tok//\"/}"
        pat="${pat//\`/}"
        pat="${pat#all:}"
        pat="${pat#./}"
        prefix=""
        IFS=/ read -ra comps <<<"$pat"
        for comp in "${comps[@]}"; do
          case "$comp" in *[\*\?\[\\]*) break ;; esac
          prefix="${prefix:+$prefix/}$comp"
        done
        out="${dir:+$dir/}$prefix"
        embed_roots_cache+="$out"$'\n'
      done
    done < <(grep -rHE --include='*.go' --exclude-dir=node_modules --exclude-dir=.git '^[[:space:]]*//go:embed[[:space:]]' "$root" 2>/dev/null || true)
  fi
  printf '%s' "$embed_roots_cache"
}

# True when the file is, or sits under, a path some //go:embed pattern embeds.
under_embedded_root() {
  local r
  while IFS= read -r r; do
    if [ -z "$r" ] || [ "$1" = "$r" ] || [[ "$1" == "$r"/* ]]; then return 0; fi
  done < <(embed_roots)
  return 1
}

# Belt and braces if the derivation above ever misses a pattern: the places this
# repository keeps data that is embedded or read by tests count as code, whatever
# their extension.
fallback_code_root() {
  case "$1" in
    data/* | automations/* | orgtemplates/* | examples/* | templates/* | testdata/* | tests/* | */templates/* | */testdata/*) return 0 ;;
  esac
  return 1
}

# True when the changed file exists, is Go, and is built only WITHOUT the
# nosocial tag (`//go:build !nosocial`): it is not part of the nosocial build, so
# changing it cannot change that build's tests. Anything else (a shared
# package, an untagged file, the nosocial stub, a deleted file we cannot
# inspect) can, so it turns the nosocial tests on.
excluded_from_nosocial() {
  [[ "$1" == *.go ]] && [ -f "$root/$1" ] &&
    grep -qE '^//go:build !nosocial([[:space:]]*&&.*)?[[:space:]]*$' "$root/$1"
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
      nosocial=1
      continue
      ;;
  esac
  case "$f" in
    *.md | docs/*)
      if [[ "$f" != *.go ]] && ! doc_is_referenced "$f" && ! under_embedded_root "$f" && ! fallback_code_root "$f"; then continue; fi
      ;;
  esac
  # Anything else is root-module code (or something we do not recognise).
  code=1
  go=1
  wails=1 # wails-app imports the root module through a replace directive
  if ! excluded_from_nosocial "$f"; then nosocial=1; fi
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
