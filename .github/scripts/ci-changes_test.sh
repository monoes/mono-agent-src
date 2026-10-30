#!/usr/bin/env bash
# Table test for ci-changes.sh: runs it against a small fixture tree, no GitHub.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
fix="$(mktemp -d)"
trap 'rm -r -- "$fix"' EXIT

mkdir -p "$fix/internal/social" "$fix/internal/plain" "$fix/cmd/tool" "$fix/internal/storage" \
  "$fix/internal/account" "$fix/docs/plans" "$fix/docs"
printf '//go:build !nosocial\n\npackage social\n' >"$fix/internal/social/a.go"
printf 'package social\n' >"$fix/internal/social/b.go"
printf 'package plain\n' >"$fix/internal/plain/p.go"
printf 'package main\n// reads README.md\n' >"$fix/cmd/tool/main.go"
printf 'package storage\n' >"$fix/internal/storage/migrate_lock_unix.go"
printf 'package account\n' >"$fix/internal/account/a.go"
printf 'package docs\n//go:embed agent-mode.md\n' >"$fix/docs/embed.go"
# Directory embeds: a new file in an embedded directory is in the binary without any Go file naming it.
mkdir -p "$fix/data/skills" "$fix/data/loose" "$fix/internal/config" "$fix/internal/tpl/templates" "$fix/pkg/ui/dist"
printf 'package data\n\n//go:embed skills\n//go:embed migrations/*.sql\n' >"$fix/data/embed.go"
printf 'package tpl\n\n//go:embed templates/*.html\n' >"$fix/internal/tpl/embed.go"
printf 'package ui\n\nvar (\n\t//go:embed all:dist\n\tdist int\n)\n' >"$fix/pkg/ui/embed.go"
printf 'package config\n' >"$fix/internal/config/c.go"
printf '//go:build nosocial\n\npackage social\n' >"$fix/internal/social/stub.go"
# Build constraints: only a constraint that PROVABLY excludes nosocial counts (no || and no parentheses).
printf '//go:build !nosocial && linux && !windows\n\npackage social\n' >"$fix/internal/social/and.go"
printf '//go:build !nosocial && linux || nosocial\n\npackage social\n' >"$fix/internal/social/or_nosocial.go"
printf '//go:build !nosocial && linux || windows\n\npackage social\n' >"$fix/internal/social/or_windows.go"
printf '//go:build (!nosocial)\n\npackage social\n' >"$fix/internal/social/paren.go"
printf '// +build !nosocial\n\npackage social\n' >"$fix/internal/social/oldstyle.go"
# Quoted and backquoted //go:embed paths with spaces.
mkdir -p "$fix/embq/a b" "$fix/embq/c d" "$fix/embq/plain"
printf 'package embq\n\n//go:embed "a b" `c d` plain\n' >"$fix/embq/embed.go"

fail=0
# check NAME FILES WANT, WANT = "go wails extension nosocial account docs_only ci_workflow"
check() {
  local name="$1" files="$2" want="$3" got
  got="$(printf '%s' "$files" | CI_CHANGES_ROOT="$fix" bash "$here/ci-changes.sh" | cut -d= -f2 | paste -sd' ')"
  if [ "$got" = "$want" ]; then
    printf 'ok    %-42s %s\n' "$name" "$got"
  else
    printf 'FAIL  %-42s got [%s] want [%s]\n' "$name" "$got" "$want"
    fail=1
  fi
}

#     name                                 files                                         go    wails ext   nosoc acct  docs  ciwf
check "empty list = everything on"         ""                                            "true true true true true false true"
check "docs only (plan + top-level md)"    $'docs/plans/x.md\nNOTES.md'                  "false false false false false true false"
check "doc a Go file names is code"        $'README.md'                                  "true true false true false false false"
check "embedded doc (docs/embed.go)"       $'docs/agent-mode.md'                         "true true false true false false false"
check "wails frontend only"                $'wails-app/frontend/src/a.ts'                "false true false false false false false"
check "wails Go only"                      $'wails-app/main.go'                          "false true false false false false false"
check "extension only"                     $'chrome-extension/background.js'             "true true true true false false false"
check "go, plain package: nosocial runs"    $'internal/plain/p.go'                        "true true false true false false false"
check "shared dependency (config): nosocial" $'internal/config/c.go'                     "true true false true false false false"
check "go, untagged file in social pkg"    $'internal/social/b.go'                       "true true false true false false false"
check "go, !nosocial-only file: no nosocial" $'internal/social/a.go'                     "true true false false false false false"
check "constraint !nosocial && tags (and only &&)" $'internal/social/and.go'             "true true false false false false false"
check "constraint !nosocial && linux || nosocial" $'internal/social/or_nosocial.go'      "true true false true false false false"
check "constraint !nosocial && linux || windows" $'internal/social/or_windows.go'        "true true false true false false false"
check "constraint (!nosocial) is conservative" $'internal/social/paren.go'               "true true false true false false false"
check "old // +build !nosocial is conservative" $'internal/social/oldstyle.go'           "true true false true false false false"
check "new md under quoted embed with a space" $'embq/a b/new.md'                        "true true false true false false false"
check "new md under backquoted embed with a space" $'embq/c d/new.md'                    "true true false true false false false"
check "new md under unquoted embed beside quoted" $'embq/plain/new.md'                   "true true false true false false false"
check "md in embq/, not embedded, stays docs" $'embq/readme.md'                          "false false false false false true false"
check "go, nosocial stub file"             $'internal/social/stub.go'                    "true true false true false false false"
check "go, deleted file (cannot inspect)"  $'internal/social/gone.go'                    "true true false true false false false"
check "social-only file + shared package"  $'internal/social/a.go\ninternal/config/c.go' "true true false true false false false"
check "go, entry point (cmd)"              $'cmd/tool/main.go'                           "true true false true true false false"
check "go, account package"                $'internal/account/a.go'                      "true true false true true false false"
check "migration file"                     $'internal/storage/migrations/001.sql'        "true true false true true false false"
check "storage lock file"                  $'internal/storage/migrate_lock_unix.go'      "true true false true true false false"
check "NEW md in embedded dir (data/skills)" $'data/skills/new-example.md'               "true true false true false false false"
check "NEW md in nested embedded dir"      $'data/skills/sub/deep/new.md'                "true true false true false false false"
check "md under glob-embed prefix"         $'data/migrations/readme.md'                  "true true false true false false false"
check "md in templates/*.html dir"         $'internal/tpl/templates/note.md'             "true true false true false false false"
check "md in all: embedded dir"            $'pkg/ui/dist/notes.md'                       "true true false true false false false"
check "md in data/ outside any embed (fallback)" $'data/loose/x.md'                      "true true false true false false false"
check "md in docs/ outside any embed stays docs" $'docs/plans/new-plan.md'               "false false false false false true false"
check "md beside embed.go, not embedded"   $'docs/other.md'                              "false false false false false true false"
check "workflow change"                    $'.github/workflows/ci.yml'                   "true true true true true false true"
check "this script changes"                $'.github/scripts/ci-changes.sh'              "true true true true true false true"
check "go.mod change"                      $'go.mod'                                     "true true true true true false true"
check "go.sum change"                      $'go.sum'                                     "true true true true true false true"
check "wails-app/go.mod change"            $'wails-app/go.mod'                           "true true true true true false true"
check "unknown path fails safe"            $'somewhere/new.txt'                          "true true false true false false false"
check "docs + one go file"                 $'docs/plans/x.md\ninternal/plain/p.go'       "true true false true false false false"
check "./ prefix and blank lines"          $'\n./docs/plans/x.md\n'                      "false false false false false true false"

# The changes job computes the list with git; a rename must list BOTH paths, or
# moving a .go file to notes.md looks like a docs-only change. This runs the very
# command ci.yml uses (extracted from it, so the two cannot drift apart).
cmd="$(grep -o 'git diff --no-renames --name-only "\$BASE_SHA" HEAD' "$here/../workflows/ci.yml" | head -1 || true)"
if [ -z "$cmd" ]; then
  echo "FAIL  ci.yml no longer computes the file list with git diff --no-renames --name-only"
  fail=1
else
  g="$(mktemp -d)"
  trap 'find "$fix" "$g" -mindepth 1 -delete; rmdir "$fix" "$g"' EXIT
  git_in() { git -C "$g" -c user.name=t -c user.email=t@t "$@"; }
  git_in init -q
  mkdir -p "$g/internal/plain" "$g/docs"
  printf 'package plain\n' >"$g/internal/plain/p.go"
  printf '# n\n' >"$g/docs/n.md"
  git_in add -A
  git_in commit -q -m base
  base="$(git_in rev-parse HEAD)"
  # scenario NAME SETUP-COMMAND WANT: change the tree, diff it the way ci.yml does, classify
  scenario() {
    local name="$1" setup="$2" want="$3" files
    git_in reset -q --hard "$base"
    (cd "$g" && eval "$setup")
    git_in add -A
    git_in commit -q -m change
    files="$(cd "$g" && BASE_SHA="$base" eval "$cmd")"
    check "$name" "$files" "$want"
  }
  scenario "rename .go -> .md still runs Go"  'git mv internal/plain/p.go notes.md'        "true true false true false false false"
  scenario "rename .md -> .go runs Go"        'git mv docs/n.md internal/plain/n.go'       "true true false true false false false"
  scenario "delete of a .go file runs Go"     'git rm -q internal/plain/p.go'              "true true false true false false false"
  scenario "rename .md -> .md stays docs"     'git mv docs/n.md docs/m.md'                 "false false false false false true false"
fi
exit $fail
