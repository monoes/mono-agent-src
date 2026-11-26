#!/usr/bin/env bash
# verify-sshsig.sh — check manifest.json.sshsig with `ssh-keygen -Y verify`, the way install.sh
# does. The release jobs run it before they publish; it reads public data only.
#
#   verify-sshsig.sh --installer install.sh MANIFEST SIGNATURE
#       against RELEASE_SSH_PUBKEY of that installer: the key users will verify with
#   verify-sshsig.sh --pubkey-file signing-pubkey.txt MANIFEST SIGNATURE
#       against "<key id> <base64 raw public key>" (only the bootstrap release, whose new key
#       is not pinned yet)
set -euo pipefail

principal="monoagent-release"
namespace="monoagent-manifest"

die() { echo "::error::verify-sshsig: $*" >&2; exit 1; }

[ "$#" -eq 4 ] || die "usage: verify-sshsig.sh --installer FILE|--pubkey-file FILE MANIFEST SIGNATURE"
mode="$1"; keyfile="$2"; manifest="$3"; signature="$4"
for f in "$keyfile" "$manifest" "$signature"; do
  [ -s "$f" ] || die "$f is missing or empty"
done
command -v ssh-keygen >/dev/null 2>&1 || die "ssh-keygen is not installed"

case "$mode" in
  --installer)
    key="$(sed -n 's/^RELEASE_SSH_PUBKEY="\(ssh-ed25519 [A-Za-z0-9+\/=]*\)"$/\1/p' "$keyfile")"
    [ -n "$key" ] && [ "$(printf '%s\n' "$key" | wc -l | tr -d ' ')" = 1 ] \
      || die "$keyfile does not hold exactly one RELEASE_SSH_PUBKEY line"
    ;;
  --pubkey-file)
    raw="$(awk 'NR==1{print $2}' "$keyfile")"
    [ -n "$raw" ] || die "$keyfile holds no public key"
    # SSH public key blob: string "ssh-ed25519" + string <32-byte key>.
    key="ssh-ed25519 $({ printf '\000\000\000\013ssh-ed25519\000\000\000 '; printf '%s' "$raw" | base64 -d; } | base64 | tr -d '\n')"
    ;;
  *) die "unknown mode '$mode'" ;;
esac

tmp="$(mktemp -d)"
trap 'rm -f "$tmp/allowed_signers"; rmdir "$tmp"' EXIT
printf '%s namespaces="%s" %s\n' "$principal" "$namespace" "$key" > "$tmp/allowed_signers"
ssh-keygen -Y verify -f "$tmp/allowed_signers" -I "$principal" -n "$namespace" -s "$signature" < "$manifest" \
  || die "$signature does not verify $manifest with the key from $keyfile. If the signing key changed, set RELEASE_PUBKEY and RELEASE_SSH_PUBKEY in install.sh (docs/release-runbook.md, Rotation)."
