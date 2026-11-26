# shellcheck shell=bash
# install-test-isolation.sh — sourced by install-test.sh and release-e2e-test.sh.
#
# install.sh follows HOME, INSTALL_DIR, XDG_DATA_HOME and MONOAGENT_APP_DIR,
# and --app writes the desktop app and its launcher there. A test run that
# inherits one of them overwrites the caller's real install. So run_installer
# starts the installer with an empty environment, sets every destination to a
# path inside $work, and refuses to start when one resolves outside it. Sourcing
# this file also points the test's own environment at a canary tree, which
# canary_check requires to come out unchanged.
#
# Needs from the caller: $work (the scratch dir), $installer, and sha().

mkdir -p "$work/tmp"
canary="$work/canary"
canary_dirs="home/.local share config app Applications"
for f in home/.local/bin/monoagentcli share/monoagent/MonoAgent share/monoagent/monoagentcli \
  share/applications/monoagent.desktop config/sentinel app/monoagentcli Applications/MonoAgent.app/sentinel; do
  mkdir -p "$canary/$(dirname "$f")" && echo "sentinel $f" > "$canary/$f"
done
export XDG_DATA_HOME="$canary/share" XDG_CONFIG_HOME="$canary/config" MONOAGENT_APP_DIR="$canary/app" \
  MONOAGENT_TEST_APPLICATIONS_DIR="$canary/Applications" INSTALL_DIR="$canary/home/.local/bin"

canary_snapshot() {
  # shellcheck disable=SC2086
  (cd "$canary" && find $canary_dirs -print | LC_ALL=C sort | while IFS= read -r f; do
    if [ -f "$f" ]; then printf '%s %s\n' "$f" "$(sha "$f")"; else printf '%s\n' "$f"; fi
  done)
}
canary_before="$(canary_snapshot)"

# canary_check — 0 when nothing in the canary tree changed; else shows what did.
canary_check() {
  local now
  now="$(canary_snapshot)"
  [ "$now" = "$canary_before" ] && return 0
  diff <(printf '%s\n' "$canary_before") <(printf '%s\n' "$now") | sed 's/^/    /' || true
  return 1
}

inside_work() { case "$1" in */..|*/../*) return 1 ;; "$work"/*) return 0 ;; *) return 1 ;; esac; }

# check_destinations <VAR=value...> — resolve every destination the way
# install.sh does (the last assignment wins) and fail unless all are in $work.
check_destinations() {
  local a d h="" inst="" data="" conf="" appdir="" apps=""
  for a in "$@"; do
    case "$a" in
      HOME=*) h="${a#*=}" ;;
      INSTALL_DIR=*) inst="${a#*=}" ;;
      XDG_DATA_HOME=*) data="${a#*=}" ;;
      XDG_CONFIG_HOME=*) conf="${a#*=}" ;;
      MONOAGENT_APP_DIR=*) appdir="${a#*=}" ;;
      MONOAGENT_TEST_APPLICATIONS_DIR=*) apps="${a#*=}" ;;
    esac
  done
  if [ -z "$inst" ]; then
    if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then inst=/usr/local/bin; else inst="$h/.local/bin"; fi
  fi
  data="${data:-$h/.local/share}"
  for d in "$h" "$h/.local/bin" "$inst" "$data/applications" "${appdir:-$data/monoagent}" \
    "${conf:-$h/.config}" "${apps:-/Applications}"; do
    inside_work "$d" || { echo "destination '$d' is outside the work dir $work" >&2; return 1; }
  done
}

# run_installer <name> [VAR=value...] [unset:VAR...] [--app-flag]
# Runs $installer (or $INSTALLER_SCRIPT) in an empty environment with HOME=
# $work/home-<name> and INSTALL_DIR=$work/prefix-<name>; sets out and rc.
# Returns 1 (without running anything) for a destination outside $work, and 1
# when the installer reports having written outside it.
run_installer() {
  local n="$1" a v h kept
  h="$work/home-$n"
  shift
  local vars=(PATH="$PATH" TMPDIR="$work/tmp" HOME="$h" XDG_DATA_HOME="$h/.local/share"
    XDG_CONFIG_HOME="$h/.config" MONOAGENT_APP_DIR= MONOAGENT_TEST_APPLICATIONS_DIR="$h/system-applications"
    INSTALL_DIR="$work/prefix-$n" MONOAGENT_INSTALL_UNSAFE_OVERRIDES=1
    MONOAGENT_TEST_ASSET_URL_PREFIX="$base/" MONOAGENT_MANIFEST_URL="$base/manifest.json")
  local flags=()
  for a in "$@"; do
    case "$a" in
      --app-flag) flags+=(--app) ;;
      unset:*)
        kept=()
        for v in "${vars[@]}"; do case "$v" in "${a#unset:}="*) ;; *) kept+=("$v") ;; esac; done
        vars=("${kept[@]}") ;;
      *) vars+=("$a") ;;
    esac
  done
  rc=0; out=""
  check_destinations "${vars[@]}" || { echo "isolation: refusing to run the installer for '$n'" >&2; return 1; }
  out="$(env -i "${vars[@]}" bash "${INSTALLER_SCRIPT:-$installer}" ${flags[@]+"${flags[@]}"} 2>&1)" || rc=$?
  # What the installer reports it wrote must be inside $work too.
  while IFS= read -r v; do
    inside_work "$v" || { echo "isolation: '$n' wrote outside the work dir: $v" >&2; return 1; }
  done < <(printf '%s\n' "$out" | sed -n -e 's/^Installed: \(.*\) (.*)$/\1/p' \
    -e 's/^Installed the desktop app: \([^(]*[^( ]\).*$/\1/p')
}
