#!/usr/bin/env bash
# The "latest" release of the releases repository, for .github/workflows/release.yml.
#
# install.sh and `monoagentcli update` read releases/latest and accept only a signed release, so a
# release is created NOT latest and is marked latest only once its signed manifest is published and
# checked (or at once where no signing is configured).
#
#   release-latest.sh holds                 true if TAG is the latest release now, else false
#   release-latest.sh superseded            yes if a newer release is latest, else no
#   release-latest.sh same-manifest DIR     yes if the release carries exactly DIR's manifest and signatures
#   release-latest.sh verify-published DIR  fails unless every file of DIR is on the release, byte for byte
#   release-latest.sh mark                  mark TAG latest, unless a newer release already is
#
# Needs TAG, RELEASES_REPO and GH_TOKEN. An answer it cannot read fails it (no guessing). Every
# `x="$(...)" || exit 1` is deliberate: set -e does not reach inside a command substitution.
set -euo pipefail

signed_files=(manifest.json manifest.json.sig manifest.json.sshsig)
version_re='^v[0-9]+\.[0-9]+\.[0-9]+$'

die() { echo "::error::$*" >&2; exit 1; }

: "${RELEASES_REPO:?RELEASES_REPO is not set}"
[[ "${TAG:-}" =~ $version_re ]] || die "TAG is '${TAG:-}', not a vX.Y.Z release tag."

work="$(mktemp -d "${TMPDIR:-/tmp}/release-latest.XXXXXX")"
trap 'rm -r "$work"' EXIT

# The tag of the latest release; nothing when the repository has none (HTTP 404).
latest_tag() {
  local out
  if out="$(gh api "repos/${RELEASES_REPO}/releases/latest" --jq .tag_name 2>"$work/err")"; then
    [ -n "$out" ] || die "GitHub named no tag for the latest release of ${RELEASES_REPO}."
    echo "$out"
  elif ! grep -q 'HTTP 404' "$work/err"; then
    die "Cannot read the latest release of ${RELEASES_REPO}: $(cat "$work/err")"
  fi
}

# newer A B: version A is higher than version B.
newer() {
  local IFS=. i
  local a=(${1#v}) b=(${2#v})
  for i in 0 1 2; do
    if ((10#${a[i]} > 10#${b[i]})); then return 0; fi
    if ((10#${a[i]} < 10#${b[i]})); then return 1; fi
  done
  return 1
}

superseded() {
  local latest
  latest="$(latest_tag)" || exit 1
  if [ -z "$latest" ] || [ "$latest" = "$TAG" ]; then echo no; return; fi
  [[ "$latest" =~ $version_re ]] ||
    die "The latest release of ${RELEASES_REPO} is '${latest}', not a vX.Y.Z tag: leaving it. Set \"latest\" by hand (docs/release-runbook.md)."
  if newer "$latest" "$TAG"; then
    echo "::warning::${latest} is already the latest release of ${RELEASES_REPO} and is newer than ${TAG}: not moving back." >&2
    echo yes
  else
    echo no
  fi
}

same_manifest() {
  local dir="$1" names f patterns=()
  names="$(gh release view "$TAG" --repo "$RELEASES_REPO" --json assets -q '.assets[].name')" || exit 1
  for f in "${signed_files[@]}"; do
    [ -f "$dir/$f" ] || die "$dir lacks $f."
    if ! grep -qxF "$f" <<<"$names"; then echo no; return; fi
    patterns+=(--pattern "$f")
  done
  gh release download "$TAG" --repo "$RELEASES_REPO" --dir "$work/signed" "${patterns[@]}" >&2 || exit 1
  for f in "${signed_files[@]}"; do
    if ! cmp -s "$dir/$f" "$work/signed/$f"; then echo no; return; fi
  done
  echo yes
}

verify_published() {
  local dir="$1" f name count=0
  for f in "${signed_files[@]}"; do [ -f "$dir/$f" ] || die "$dir lacks $f."; done
  gh release download "$TAG" --repo "$RELEASES_REPO" --dir "$work/published" ||
    die "Cannot download release ${TAG} of ${RELEASES_REPO} to check it."
  for f in "$dir"/*; do
    name="${f##*/}"
    [ -f "$work/published/$name" ] || die "Release ${TAG} of ${RELEASES_REPO} lacks ${name}."
    cmp -s "$f" "$work/published/$name" || die "Release ${TAG} of ${RELEASES_REPO}: ${name} differs from the verified file."
    count=$((count + 1))
  done
  echo "Release ${TAG} of ${RELEASES_REPO} carries all ${count} verified files."
}

mark() {
  local state latest attempt
  state="$(superseded)" || exit 1
  [ "$state" = no ] || return 0
  latest="$(latest_tag)" || exit 1
  if [ "$latest" = "$TAG" ]; then
    echo "${TAG} is already the latest release of ${RELEASES_REPO}."
    return 0
  fi
  gh release edit "$TAG" --repo "$RELEASES_REPO" --latest ||
    die "Could not mark ${TAG} as latest in ${RELEASES_REPO}; ${latest:-no release} stays latest."
  for attempt in 1 2 3 4 5; do
    latest="$(latest_tag)" || exit 1
    if [ "$latest" = "$TAG" ]; then
      echo "${TAG} is now the latest release of ${RELEASES_REPO}."
      return 0
    fi
    sleep "${RELEASE_LATEST_RETRY_SECONDS:-5}"
  done
  die "Marked ${TAG} as latest, but ${RELEASES_REPO} still answers '${latest}'."
}

case "${1:-}:$#" in
  holds:1)
    latest="$(latest_tag)" || exit 1
    if [ "$latest" = "$TAG" ]; then echo true; else echo false; fi ;;
  superseded:1) superseded ;;
  same-manifest:2) same_manifest "$2" ;;
  verify-published:2) verify_published "$2" ;;
  mark:1) mark ;;
  *) echo "usage: see the header of $0" >&2; exit 2 ;;
esac
