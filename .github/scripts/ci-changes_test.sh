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
check "doc a Go file names is code"        $'README.md'                                  "true true false false false false false"
check "embedded doc (docs/embed.go)"       $'docs/agent-mode.md'                         "true true false false false false false"
check "wails frontend only"                $'wails-app/frontend/src/a.ts'                "false true false false false false false"
check "wails Go only"                      $'wails-app/main.go'                          "false true false false false false false"
check "extension only"                     $'chrome-extension/background.js'             "true true true false false false false"
check "go, plain package"                  $'internal/plain/p.go'                        "true true false false false false false"
check "go, tagged package (untagged file)" $'internal/social/b.go'                       "true true false true false false false"
check "go, tagged file"                    $'internal/social/a.go'                       "true true false true false false false"
check "go, deleted file in tagged package" $'internal/social/gone.go'                    "true true false true false false false"
check "go, entry point (cmd)"              $'cmd/tool/main.go'                           "true true false false true false false"
check "go, account package"                $'internal/account/a.go'                      "true true false false true false false"
check "migration file"                     $'internal/storage/migrations/001.sql'        "true true false false true false false"
check "storage lock file"                  $'internal/storage/migrate_lock_unix.go'      "true true false false true false false"
check "workflow change"                    $'.github/workflows/ci.yml'                   "true true true true true false true"
check "this script changes"                $'.github/scripts/ci-changes.sh'              "true true true true true false true"
check "go.mod change"                      $'go.mod'                                     "true true true true true false true"
check "go.sum change"                      $'go.sum'                                     "true true true true true false true"
check "wails-app/go.mod change"            $'wails-app/go.mod'                           "true true true true true false true"
check "unknown path fails safe"            $'somewhere/new.txt'                          "true true false false false false false"
check "docs + one go file"                 $'docs/plans/x.md\ninternal/plain/p.go'       "true true false false false false false"
check "./ prefix and blank lines"          $'\n./docs/plans/x.md\n'                      "false false false false false true false"
exit $fail
