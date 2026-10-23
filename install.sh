#!/usr/bin/env bash
# install.sh — install the mono-agent CLI (monoagentcli).
#
# Default path (no GitHub API needed):
#   1. fetch manifest.json from the latest GitHub release of monoes/mono-agent
#      (monoes.me is tried second)
#   2. verify the manifest's Ed25519 signature (manifest.json.sig) against the
#      pinned release key (RELEASE_PUBKEY below). This needs `openssl` with
#      Ed25519 support; without it the install is checksum-only and says so.
#      It never claims a signature was verified when it was not.
#   3. choose the "cli" asset for this host's os/arch (and, with --app, the
#      "app" asset)
#   4. download it from the asset's url
#   5. verify its sha256 against the manifest
#
# Legacy path (kept simple, labelled in the output): the GitHub releases API
# plus SHA256SUMS.txt. Used only when MONOAGENT_ALLOW_LEGACY_GITHUB=1, or
# when the manifest URL cannot be reached. A manifest that is reachable but
# fails verification is a hard error, never a fallback.
#
# Supported platforms: darwin/{arm64,amd64}, linux/{amd64,arm64}.
# Windows: download the .exe from the releases page.
#
# Flags:
#   --app         also install the desktop app (Linux amd64: into
#                 ~/.local/share/monoagent with a launcher entry; macOS arm64:
#                 MonoAgent.app into /Applications, or ~/Applications when
#                 /Applications is not writable)
#   --no-verify   skip signature and checksum verification (not recommended)
#
# Environment:
#   INSTALL_DIR                      install location. Default: /usr/local/bin
#                                    when it is writable without sudo, else
#                                    ~/.local/bin. An INSTALL_DIR you set
#                                    yourself uses sudo when it needs it.
#   MONOAGENT_APP_DIR                where --app puts the app (default above)
#   MONOAGENT_MANIFEST_URL           manifest URL (tests, mirrors); only this one is tried
#   MONOAGENT_RELEASE_PUBKEY         base64 raw Ed25519 public key; overrides RELEASE_PUBKEY
#   MONOAGENT_ALLOW_LEGACY_GITHUB=1  use the GitHub API path directly
#   MONOAGENT_TEST_APPLICATIONS_DIR  tests only: stands in for /Applications
#                                    when --app picks the macOS app folder
set -euo pipefail

# Repository used by the legacy path.
REPO="monoes/mono-agent"
BIN="monoagentcli"
# The manifest is a release asset; GitHub redirects latest/download to the
# newest release. manifest.json.sig sits next to it under the same name + .sig.
if [ -n "${MONOAGENT_MANIFEST_URL:-}" ]; then
  MANIFEST_URLS="$MONOAGENT_MANIFEST_URL"
else
  MANIFEST_URLS="https://github.com/${REPO}/releases/latest/download/manifest.json https://monoes.me/mono-agent/releases/latest/manifest.json"
fi
MANIFEST_URL=""   # the one that answered, set by install_from_manifest
# Pinned manifest-signing public key: base64 of the raw 32-byte Ed25519 key,
# as printed by `monoagentcli release keygen`. Key id 3b3e972f459a3151; the
# same key is pinned in internal/release/keys.go.
RELEASE_PUBKEY="/GDrvFCQb64EBe+tYDkFWNFXeiwysdr4sAB8kc+b2kU="
RELEASE_PUBKEY="${MONOAGENT_RELEASE_PUBKEY:-$RELEASE_PUBKEY}"

err() { printf 'install.sh: error: %s\n' "$*" >&2; exit 1; }

NO_VERIFY=0
WITH_APP=0
for arg in "$@"; do
  case "$arg" in
    --no-verify) NO_VERIFY=1 ;;
    --app)       WITH_APP=1 ;;
    *)            err "unknown option '$arg' (supported: --app, --no-verify)" ;;
  esac
done

# Default install dir: /usr/local/bin only when this user can write it without
# sudo, else ~/.local/bin. An INSTALL_DIR the user set is kept as given (sudo
# is used for it below when needed).
if [ -z "${INSTALL_DIR:-}" ]; then
  if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
    INSTALL_DIR="/usr/local/bin"
  else
    INSTALL_DIR="${HOME}/.local/bin"
  fi
fi

command -v curl >/dev/null 2>&1 || err "curl is required but not installed"

# sha256_of <file> — print the hex sha256 of a file.
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    err "neither sha256sum nor shasum is available to verify the download"
  fi
}

# verify_checksum <dir> <asset> — legacy path: check <dir>/<asset> against the
# <asset> line in <dir>/SHA256SUMS.txt ("<sha256>  <filename>"). Prints the
# verified digest; returns non-zero (message on stderr) on failure.
verify_checksum() {
  v_dir="$1"
  v_asset="$2"
  v_line="$(grep "[[:space:]]${v_asset}\$" "${v_dir}/SHA256SUMS.txt" | head -n 1 || true)"
  if [ -z "$v_line" ]; then
    printf 'install.sh: error: no checksum entry for %s in SHA256SUMS.txt\n' "$v_asset" >&2
    return 1
  fi
  v_want="$(printf '%s' "$v_line" | awk '{print $1}')"
  v_got="$(sha256_of "${v_dir}/${v_asset}")"
  if [ "$v_got" != "$v_want" ]; then
    printf 'install.sh: error: checksum mismatch for %s (expected %s)\n' "$v_asset" "$v_want" >&2
    return 1
  fi
  printf '%s\n' "$v_want"
}

# json_field <object> <key> — value of a flat string/number field.
json_field() {
  printf '%s' "$1" | sed -n 's/.*"'"$2"'" *: *"\{0,1\}\([^",}]*\)"\{0,1\}.*/\1/p' | head -n 1
}

# asset_url_allowed <url> — the asset must come over https from monoes.me,
# github.com or GitHub's release-asset hosts (same allow-list as the Go
# client). MONOAGENT_TEST_ASSET_URL_PREFIX (tests only) additionally allows
# URLs that start with that exact prefix.
asset_url_allowed() {
  if [ -n "${MONOAGENT_TEST_ASSET_URL_PREFIX:-}" ]; then
    case "$1" in "${MONOAGENT_TEST_ASSET_URL_PREFIX}"*) return 0 ;; esac
  fi
  case "$1" in https://*) ;; *) return 1 ;; esac
  u_host="${1#https://}"
  u_host="${u_host%%[/?#]*}"
  case "$u_host" in
    monoes.me|github.com|objects.githubusercontent.com|release-assets.githubusercontent.com) return 0 ;;
    *) return 1 ;;
  esac
}

# verify_manifest_signature <manifest> <sig-url> <tmp> — returns 0 verified,
# 2 skipped (no key / no openssl Ed25519; message printed), 1 failed.
verify_manifest_signature() {
  m_file="$1"; m_sigurl="$2"; m_tmp="$3"
  if [ -z "$RELEASE_PUBKEY" ]; then
    echo "NOTE: no release signing key is configured in this installer; manifest signature verification is SKIPPED (checksum-only)." >&2
    return 2
  fi
  if ! command -v openssl >/dev/null 2>&1; then
    echo "NOTE: openssl not found; manifest signature verification is SKIPPED (checksum-only)." >&2
    return 2
  fi
  {
    printf -- '-----BEGIN PUBLIC KEY-----\n'
    printf 'MCowBQYDK2VwAyEA%s\n' "$RELEASE_PUBKEY"
    printf -- '-----END PUBLIC KEY-----\n'
  } > "${m_tmp}/pub.pem"
  if ! openssl pkey -pubin -in "${m_tmp}/pub.pem" -noout >/dev/null 2>&1; then
    echo "NOTE: this openssl cannot read Ed25519 keys; manifest signature verification is SKIPPED (checksum-only)." >&2
    return 2
  fi
  # Verifying needs pkeyutl -rawin (OpenSSL 1.1.1+). An openssl that reads the
  # key but lacks it (such as a LibreSSL build) would fail every signature, so
  # it is treated like one without Ed25519.
  if ! openssl pkeyutl -help 2>&1 | grep -q -e '-rawin'; then
    echo "NOTE: this openssl cannot verify raw Ed25519 signatures (no pkeyutl -rawin); manifest signature verification is SKIPPED (checksum-only)." >&2
    return 2
  fi
  curl --retry 3 --fail --location -fsSL -o "${m_tmp}/manifest.json.sig" "$m_sigurl" || return 1
  # Contract: ONE line "<keyid> <base64sig>". keyid is the hex of the first 8
  # bytes of SHA-256 of the raw public key. Anything else is malformed.
  m_nfields="$(awk 'NR==1{print NF}' "${m_tmp}/manifest.json.sig")"
  if [ "$m_nfields" != "2" ] || [ "$(wc -l < "${m_tmp}/manifest.json.sig" | tr -d ' ')" -gt 1 ]; then
    echo "install.sh: malformed manifest.json.sig (expected '<keyid> <base64sig>')" >&2
    return 1
  fi
  m_keyid="$(printf '%s' "$RELEASE_PUBKEY" | openssl base64 -d -A 2>/dev/null \
    | openssl dgst -sha256 -binary | od -An -tx1 | tr -d ' \n' | cut -c1-16)"
  m_sigkeyid="$(awk 'NR==1{print $1}' "${m_tmp}/manifest.json.sig")"
  if [ -z "$m_keyid" ] || [ "$m_sigkeyid" != "$m_keyid" ]; then
    echo "install.sh: manifest signed with key id '${m_sigkeyid}', not the pinned key '${m_keyid}'" >&2
    return 1
  fi
  awk 'NR==1{print $2}' "${m_tmp}/manifest.json.sig" | openssl base64 -d -A > "${m_tmp}/sig.bin" 2>/dev/null || return 1
  openssl pkeyutl -verify -pubin -inkey "${m_tmp}/pub.pem" -rawin \
    -in "$m_file" -sigfile "${m_tmp}/sig.bin" >/dev/null 2>&1 || return 1
  return 0
}

# ── Detect platform ──────────────────────────────────────────────────────────
OS="$(uname -s)"
ARCH="$(uname -m)"

case "$OS" in
  Darwin) os="darwin" ;;
  Linux)  os="linux" ;;
  *)      err "unsupported OS '$OS' (supported: darwin, linux). Windows: download monoagentcli-windows-amd64.exe from the releases page." ;;
esac

case "$ARCH" in
  arm64|aarch64) arch="arm64" ;;
  amd64|x86_64)  arch="amd64" ;;
  *)             err "unsupported architecture '$ARCH' on $os" ;;
esac

ASSET="monoagentcli-${os}-${arch}"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT
SRC=""   # the downloaded file to install, set by either path
APP_SRC=""   # the downloaded desktop app archive (--app), set by either path
TAG=""

# ── Manifest path ────────────────────────────────────────────────────────────
# manifest_fetch_asset <kind> — download this host's <kind> ("cli" or "app")
# asset named in the verified manifest into $TMP_DIR and check its sha256.
# Sets A_NAME and A_FILE.
manifest_fetch_asset() {
  f_kind="$1"
  entry=""
  # Asset objects hold no nested braces, so each is a {...} with no inner { or }.
  while IFS= read -r obj; do
    if [ "$(json_field "$obj" os)" = "$os" ] && [ "$(json_field "$obj" arch)" = "$arch" ] \
       && [ "$(json_field "$obj" kind)" = "$f_kind" ]; then
      entry="$obj"; break
    fi
  done < <(printf '%s' "$flat" | grep -o '{[^{}]*}' || true)
  if [ -z "$entry" ]; then
    [ "$f_kind" = cli ] && err "the manifest has no CLI asset for ${os}/${arch}"
    err "the release has no desktop app for ${os}/${arch} (apps are built for linux/amd64, darwin/arm64 and windows/amd64)"
  fi
  A_NAME="$(json_field "$entry" name)"
  a_url="$(json_field "$entry" url)"
  a_sha="$(json_field "$entry" sha256)"
  [ -n "$A_NAME" ] && [ -n "$a_url" ] || err "the manifest entry for ${os}/${arch} is incomplete"
  asset_url_allowed "$a_url" \
    || err "the manifest asset url '${a_url}' is not https on an allowed host (monoes.me, github.com, GitHub release assets). Nothing was installed."
  case "$A_NAME" in */*|..*) err "the manifest asset name '${A_NAME}' is not a plain file name" ;; esac

  echo "Downloading ${A_NAME} (${TAG:-unknown version})..."
  A_FILE="${TMP_DIR}/${A_NAME}"
  curl --retry 3 --fail --location --progress-bar -o "$A_FILE" "$a_url" \
    || err "download failed: $a_url"

  if [ "$NO_VERIFY" -ne 1 ]; then
    [ -n "$a_sha" ] || err "the manifest gives no sha256 for ${A_NAME}"
    got="$(sha256_of "$A_FILE")"
    [ "$got" = "$a_sha" ] \
      || err "checksum mismatch for ${A_NAME} (manifest ${a_sha}, downloaded ${got}). Nothing was installed."
    echo "Checksum verified: ${A_NAME} SHA256 ${got}"
  fi
}

# Returns 10 when no manifest URL can be fetched (caller may fall back);
# any other failure exits.
install_from_manifest() {
  for u in $MANIFEST_URLS; do
    echo "Fetching release manifest: ${u}"
    if curl --retry 3 --fail --location -fsSL -o "${TMP_DIR}/manifest.json" "$u"; then
      MANIFEST_URL="$u"; break
    fi
  done
  [ -n "$MANIFEST_URL" ] || return 10

  if [ "$NO_VERIFY" -eq 1 ]; then
    echo "WARNING: --no-verify given; skipping signature and checksum verification." >&2
  else
    sig_status=0
    verify_manifest_signature "${TMP_DIR}/manifest.json" "${MANIFEST_URL}.sig" "$TMP_DIR" || sig_status=$?
    case "$sig_status" in
      0) echo "Manifest signature verified (Ed25519, key ${m_keyid})." ;;
      2) ;;
      *) err "manifest signature verification FAILED; the manifest is not signed by the pinned release key. Nothing was installed." ;;
    esac
  fi

  flat="$(tr '\n\r\t' '   ' < "${TMP_DIR}/manifest.json")"
  TAG="$(json_field "$flat" version)"
  # expires_at (optional, covered by the signature): RFC 3339 UTC as written by
  # cmd/release-manifest. Same UTC layout, so a string compare orders it. Any
  # other layout is refused rather than guessed at.
  if [ "$NO_VERIFY" -ne 1 ]; then
    m_exp="$(json_field "$flat" expires_at)"
    if [ -n "$m_exp" ]; then
      printf '%s' "$m_exp" | grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$' \
        || err "the manifest expires_at '${m_exp}' is not a UTC RFC 3339 time. Nothing was installed."
      m_now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
      [ "$m_exp" \> "$m_now" ] \
        || err "the manifest expired at ${m_exp}; ask the maintainers for a fresh release. Nothing was installed."
    fi
  fi

  manifest_fetch_asset cli
  case "$A_NAME" in
    *.tar.gz|*.tgz)
      command -v tar >/dev/null 2>&1 || err "tar is required to unpack ${A_NAME}"
      mkdir -p "${TMP_DIR}/x"
      tar -xzf "$A_FILE" -C "${TMP_DIR}/x" || err "could not unpack ${A_NAME}"
      SRC="$(find "${TMP_DIR}/x" -type f -name "$BIN" | head -n 1)"
      [ -n "$SRC" ] || err "${A_NAME} does not contain ${BIN}"
      ;;
    *) SRC="$A_FILE" ;;
  esac

  if [ "$WITH_APP" -eq 1 ]; then
    manifest_fetch_asset app
    APP_SRC="$A_FILE"
  fi
}

# ── Legacy path: GitHub API + SHA256SUMS.txt ─────────────────────────────────
install_from_github_legacy() {
  echo "Using the legacy GitHub release path for ${REPO} (no signed manifest)."
  RELEASE_JSON="$(curl --retry 3 --fail --location -fsSL "https://api.github.com/repos/${REPO}/releases/latest")" \
    || err "could not reach the GitHub API (network down, rate-limited, or the repository is not public)"
  TAG="$(printf '%s' "$RELEASE_JSON" | grep -o '"tag_name": *"[^"]*"' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')"
  [ -n "$TAG" ] || err "could not parse the latest tag from the GitHub API response"
  URL="https://github.com/${REPO}/releases/download/${TAG}/${ASSET}"

  echo "Downloading ${ASSET} (${TAG})..."
  curl --retry 3 --fail --location --progress-bar -o "${TMP_DIR}/${ASSET}" "$URL" \
    || err "download failed: $URL (does this release include the ${ASSET} asset?)"

  if [ "$NO_VERIFY" -eq 1 ]; then
    echo "WARNING: --no-verify given; skipping checksum verification." >&2
  else
    SUMS_URL="https://github.com/${REPO}/releases/download/${TAG}/SHA256SUMS.txt"
    curl --retry 3 --fail --location -fsSL -o "${TMP_DIR}/SHA256SUMS.txt" "$SUMS_URL" \
      || err "SHA256SUMS.txt not found at ${SUMS_URL}. Refusing to install an unverified binary; re-run with --no-verify to override at your own risk."
    VERIFIED="$(verify_checksum "${TMP_DIR}" "${ASSET}")" \
      || err "checksum verification FAILED for ${ASSET}; the download does not match ${TAG}'s published digest. Nothing was installed."
    echo "Checksum verified: ${ASSET} SHA256 ${VERIFIED}"
  fi
  SRC="${TMP_DIR}/${ASSET}"

  if [ "$WITH_APP" -eq 1 ]; then
    case "${os}/${arch}" in
      linux/amd64)  app_asset="MonoAgent-linux-amd64.tar.gz" ;;
      darwin/arm64) app_asset="MonoAgent-darwin-arm64.zip" ;;
      *) err "the release has no desktop app for ${os}/${arch} (apps are built for linux/amd64, darwin/arm64 and windows/amd64)" ;;
    esac
    echo "Downloading ${app_asset} (${TAG})..."
    curl --retry 3 --fail --location --progress-bar -o "${TMP_DIR}/${app_asset}" \
      "https://github.com/${REPO}/releases/download/${TAG}/${app_asset}" \
      || err "download failed: ${app_asset}"
    if [ "$NO_VERIFY" -ne 1 ]; then
      VERIFIED="$(verify_checksum "${TMP_DIR}" "${app_asset}")" \
        || err "checksum verification FAILED for ${app_asset}; the desktop app was not installed."
      echo "Checksum verified: ${app_asset} SHA256 ${VERIFIED}"
    fi
    APP_SRC="${TMP_DIR}/${app_asset}"
  fi
}

if [ "${MONOAGENT_ALLOW_LEGACY_GITHUB:-0}" = "1" ]; then
  install_from_github_legacy
else
  rc=0
  install_from_manifest || rc=$?
  if [ "$rc" -eq 10 ]; then
    echo "Manifest unreachable (${MANIFEST_URLS}); falling back to the legacy GitHub path." >&2
    install_from_github_legacy
  fi
fi

chmod +x "$SRC"

# ── Install ──────────────────────────────────────────────────────────────────
# sudo only when the install dir (or, if it does not exist yet, its closest
# existing parent) is not writable by this user — e.g. INSTALL_DIR=~/.local/bin
# never needs it.
writable_target() {
  d="$1"
  while [ ! -d "$d" ]; do
    parent="$(dirname "$d")"
    [ "$parent" = "$d" ] && return 1
    d="$parent"
  done
  [ -w "$d" ]
}
SUDO=""
if [ "$(id -u)" != "0" ] && ! writable_target "$INSTALL_DIR"; then
  command -v sudo >/dev/null 2>&1 \
    || err "${INSTALL_DIR} is not writable by $(id -un) and sudo is not available; re-run as root, or choose a writable directory with INSTALL_DIR=\$HOME/.local/bin"
  SUDO="sudo"
fi

$SUDO mkdir -p "$INSTALL_DIR" || err "cannot create ${INSTALL_DIR}"
$SUDO mv "$SRC" "${INSTALL_DIR}/${BIN}" || err "cannot install into ${INSTALL_DIR}"

echo "Installed: ${INSTALL_DIR}/${BIN} (${TAG:-unknown version}, ${os}/${arch})"

# ── Desktop app (--app) ──────────────────────────────────────────────────────
# Linux: the tarball's MonoAgent-linux-amd64 and its bundled monoagentcli go
# side by side into ~/.local/share/monoagent (the app runs the CLI next to it),
# with a ~/.local/bin/monoagent link and a launcher entry. macOS: MonoAgent.app
# into /Applications, or ~/Applications when that is not writable. No sudo.
install_app() {
  case "$os" in
    linux)
      app_dir="${MONOAGENT_APP_DIR:-${XDG_DATA_HOME:-${HOME}/.local/share}/monoagent}"
      mkdir -p "${TMP_DIR}/app" "$app_dir" "${HOME}/.local/bin" \
        || err "cannot create ${app_dir}"
      tar -xzf "$APP_SRC" -C "${TMP_DIR}/app" MonoAgent-linux-amd64 monoagentcli \
        || err "could not unpack the desktop app"
      chmod +x "${TMP_DIR}/app/MonoAgent-linux-amd64" "${TMP_DIR}/app/monoagentcli"
      mv -f "${TMP_DIR}/app/MonoAgent-linux-amd64" "${app_dir}/MonoAgent" \
        && mv -f "${TMP_DIR}/app/monoagentcli" "${app_dir}/monoagentcli" \
        || err "cannot install into ${app_dir}"
      ln -sfn "${app_dir}/MonoAgent" "${HOME}/.local/bin/monoagent"
      apps_dir="${XDG_DATA_HOME:-${HOME}/.local/share}/applications"
      mkdir -p "$apps_dir"
      cat > "${apps_dir}/monoagent.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=MonoAgent
GenericName=Workflow Automation
Exec=${app_dir}/MonoAgent
Icon=monoagent
Terminal=false
Categories=Development;Utility;
StartupWMClass=monoagent
DESKTOP
      if command -v update-desktop-database >/dev/null 2>&1; then
        update-desktop-database "$apps_dir" >/dev/null 2>&1 || true
      fi
      echo "Installed the desktop app: ${app_dir}/MonoAgent (launcher: MonoAgent, command: monoagent)"
      ;;
    darwin)
      command -v unzip >/dev/null 2>&1 || err "unzip is required to unpack the desktop app"
      if [ -n "${MONOAGENT_APP_DIR:-}" ]; then
        app_dir="$MONOAGENT_APP_DIR"
      elif [ -w "${MONOAGENT_TEST_APPLICATIONS_DIR:-/Applications}" ]; then
        app_dir="${MONOAGENT_TEST_APPLICATIONS_DIR:-/Applications}"
      else
        app_dir="${HOME}/Applications"
      fi
      mkdir -p "${TMP_DIR}/app" "$app_dir" || err "cannot create ${app_dir}"
      unzip -q "$APP_SRC" -d "${TMP_DIR}/app" || err "could not unpack the desktop app"
      [ -d "${TMP_DIR}/app/MonoAgent.app" ] || err "the desktop app archive has no MonoAgent.app"
      # Move the old bundle into the temp dir (cleaned on exit) rather than
      # deleting it in place, so a failed move leaves the old app working.
      if [ -e "${app_dir}/MonoAgent.app" ]; then
        mv "${app_dir}/MonoAgent.app" "${TMP_DIR}/MonoAgent.app.old" || err "cannot replace ${app_dir}/MonoAgent.app"
      fi
      if ! mv "${TMP_DIR}/app/MonoAgent.app" "${app_dir}/MonoAgent.app"; then
        [ -e "${TMP_DIR}/MonoAgent.app.old" ] && mv "${TMP_DIR}/MonoAgent.app.old" "${app_dir}/MonoAgent.app"
        err "cannot install into ${app_dir}"
      fi
      echo "Installed the desktop app: ${app_dir}/MonoAgent.app"
      ;;
  esac
}
if [ -n "$APP_SRC" ]; then install_app; fi

# ── PATH hint ────────────────────────────────────────────────────────────────
case ":${PATH}:" in
  *":${INSTALL_DIR}:"*) ;;
  *)
    echo
    echo "${INSTALL_DIR} is not on your PATH. Add it, then open a new terminal:"
    echo "  bash:  echo 'export PATH=\"${INSTALL_DIR}:\$PATH\"' >> ~/.bashrc"
    echo "  zsh:   echo 'export PATH=\"${INSTALL_DIR}:\$PATH\"' >> ~/.zshrc"
    echo "  fish:  fish_add_path ${INSTALL_DIR}"
    echo
    ;;
esac

# An older copy elsewhere on PATH (e.g. a previous sudo install into
# /usr/local/bin) would shadow this one.
found="$(command -v "${BIN}" 2>/dev/null || true)"
if [ -n "$found" ] && [ "$found" != "${INSTALL_DIR}/${BIN}" ]; then
  echo
  echo "Note: ${found} comes first on your PATH and is not the copy just installed."
  echo "Remove it (it may need sudo) or put ${INSTALL_DIR} earlier on your PATH."
  echo
fi

echo "Verify with:  ${INSTALL_DIR}/${BIN} version"
echo "Sign in to monoes.me (required; nothing else works before it):  ${INSTALL_DIR}/${BIN} account login"
echo "Account and offline behavior:  ${INSTALL_DIR}/${BIN} ref account"
echo "Browser extension (ships with the CLI; unpacks it, then Load unpacked in chrome://extensions):  ${INSTALL_DIR}/${BIN} extension install"
