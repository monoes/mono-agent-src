#!/usr/bin/env bash
# Fails when a Go binary that is about to ship was built with the devaccount, releasee2e or e2estub tag
# (spec D11, D24): devaccount trusts a development signing key anyone can sign with, releasee2e pins
# a test update key and redirects the update client to a loopback server. It reads the build settings every
# Go binary carries (`go version -m`), whatever OS it was built for.
#
#   scripts/check-release-tags.sh [--min N] [--allow-non-go NAME]... <file-or-folder>...
#
# A folder is searched; a .tar.gz, .tgz or .zip is unpacked and searched too, so the desktop app's
# executable and the CLI bundled with it are covered. Files that are not executables are ignored;
# a file that looks like an executable (ELF, Mach-O, PE magic) but has no readable Go build info is
# an error unless its base name was passed with --allow-non-go NAME (repeatable).
# --min N fails when fewer than N Go binaries were found, so a release whose layout changed cannot
# pass by checking nothing.
set -euo pipefail

forbidden_tags="devaccount releasee2e e2estub"
min=1
allow=" "
while [ "$#" -gt 0 ]; do
  case "$1" in
    --min) min="${2:?--min needs a number}"; shift 2 ;;
    --allow-non-go) allow="$allow${2:?--allow-non-go needs a name} "; shift 2 ;;
    *) break ;;
  esac
done
[ "$#" -gt 0 ] || { echo "usage: check-release-tags.sh [--min N] [--allow-non-go NAME]... <file-or-folder>..." >&2; exit 2; }
command -v go >/dev/null || { echo "check-release-tags: go is required" >&2; exit 2; }

scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT
checked=0
bad=0
bad_nongo=0

# looks_executable reports whether the file starts with an ELF, Mach-O (thin, fat or either byte
# order) or PE ("MZ") magic.
looks_executable() {
  local magic
  magic="$(head -c 4 "$1" 2>/dev/null | od -An -tx1 | tr -d ' \n')"
  case "$magic" in
    7f454c46 | feedface | feedfacf | cefaedfe | cffaedfe | cafebabe | bebafeca) return 0 ;;
    4d5a*) return 0 ;;
  esac
  return 1
}

# check inspects one file: a Go binary is counted and judged, an archive is unpacked and each
# file inside is checked, anything else is skipped. label is the name to report.
check() {
  local f="$1" label="$2" info tags dir
  case "$f" in
    *.tar.gz | *.tgz | *.zip)
      dir="$(mktemp -d "$scratch/unpack.XXXXXX")"
      if [ "${f##*.}" = "zip" ]; then unzip -q "$f" -d "$dir"; else tar -xzf "$f" -C "$dir"; fi
      while IFS= read -r inner; do check "$inner" "$label:${inner#"$dir"/}"; done < <(find "$dir" -type f | sort)
      return
      ;;
  esac
  info="$(go version -m "$f" 2>/dev/null)" || info=""
  if [ -z "$info" ]; then
    if looks_executable "$f"; then
      case "$allow" in
        *" $(basename "$f") "*) echo "ok: $label (allowed non-Go executable)" ;;
        *)
          echo "::error::$label looks like an executable but is not a readable Go binary" >&2
          bad_nongo=$((bad_nongo + 1))
          ;;
      esac
    fi
    return 0
  fi
  checked=$((checked + 1))
  tags="$(printf '%s\n' "$info" | awk -F'\t' '$2 == "build" && $3 ~ /^-tags=/ { sub(/^-tags=/, "", $3); print $3 }')"
  hit=""
  for t in $forbidden_tags; do
    case ",$tags," in *",$t,"*) hit="$t" ;; esac
  done
  if [ -n "$hit" ]; then
    echo "::error::$label was built with the $hit tag (-tags=$tags): a release must never carry it" >&2
    bad=$((bad + 1))
  else
    echo "ok: $label (tags: ${tags:-none})"
  fi

}

for arg in "$@"; do
  if [ -d "$arg" ]; then
    while IFS= read -r f; do check "$f" "${f#"$arg"/}"; done < <(find "$arg" -type f | sort)
  elif [ -f "$arg" ]; then
    check "$arg" "$(basename "$arg")"
  else
    echo "check-release-tags: $arg does not exist" >&2
    exit 2
  fi
done

if [ "$bad_nongo" -gt 0 ]; then
  echo "check-release-tags: $bad_nongo executable(s) have no readable Go build info (pass --allow-non-go NAME for a legitimate one)" >&2
  exit 1
fi
if [ "$bad" -gt 0 ]; then
  echo "check-release-tags: $bad binary(ies) carry the $forbidden tag" >&2
  exit 1
fi
if [ "$checked" -lt "$min" ]; then
  echo "::error::check-release-tags: found $checked Go binaries, expected at least $min: the release layout changed, or an archive did not unpack" >&2
  exit 1
fi
echo "check-release-tags: $checked Go binaries, none carries a forbidden tag ($forbidden_tags)"
