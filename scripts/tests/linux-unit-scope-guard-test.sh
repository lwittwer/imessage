#!/usr/bin/env bash
# Synthetic regression test for the Linux setup scripts' duplicate-unit guard.
# Fake systemctl reports no user bus and optionally a system-scope unit; no
# service manager, bridge state, or real home directory is touched.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

mkdir -p "$TMP/bin"
cat > "$TMP/bin/id" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in
    -u) echo 987654321 ;;
    -un) echo synthetic-user ;;
    *) exit 1 ;;
esac
EOF
cat > "$TMP/bin/systemctl" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$SYSTEMCTL_CALLS"
if [ "${1:-}" = "--user" ]; then
    if [ "${2:-}" = "cat" ] && [ "${FAKE_USER_MANAGER_UNIT:-false}" = true ]; then
        exit 0
    fi
    if [ "${2:-}" = "show-environment" ] && [ "${FAKE_USER_BUS:-false}" = true ]; then
        exit 0
    fi
elif [ "${1:-}" = "cat" ] && [ "${FAKE_SYSTEM_UNIT:-false}" = true ]; then
    exit 0
fi
exit 1
EOF
chmod +x "$TMP/bin/id" "$TMP/bin/systemctl"

run_refusal_case() {
    local script="$1" case_name="$2" system_unit="$3" expected="$4"
    local unit_location="${5:-default}" custom_xdg="${6:-false}"
    local home="$TMP/$case_name-home" data="$TMP/$case_name-data" calls="$TMP/$case_name-systemctl.log"
    local unit_config="$home/.config"
    if [ "$unit_location" = custom ]; then
        unit_config="$home/custom-config"
    fi
    mkdir -p "$unit_config/systemd/user" "$home/.config/systemd/user" "$data"
    : > "$unit_config/systemd/user/corten-matrix.service"
    : > "$calls"

    local output="$TMP/$case_name-output.log" status=0 xdg_config_home=""
    if [ "$custom_xdg" = true ]; then
        xdg_config_home="$home/custom-config"
    fi
    HOME="$home" PATH="$TMP/bin:$PATH" XDG_RUNTIME_DIR= XDG_CONFIG_HOME="$xdg_config_home" SUDO_USER= \
        SYSTEMCTL_CALLS="$calls" FAKE_USER_BUS=false FAKE_SYSTEM_UNIT="$system_unit" \
        bash "$ROOT/$script" /bin/true "$data" >"$output" 2>&1 || status=$?

    if [ "$status" -eq 0 ]; then
        echo "FAIL: $script accepted $case_name" >&2
        cat "$output" >&2
        return 1
    fi
    if ! grep -Fq "$expected" "$output"; then
        echo "FAIL: $script did not report '$expected' for $case_name" >&2
        cat "$output" >&2
        return 1
    fi
    if grep -Eq '(^| )(stop|restart|enable)( |$)' "$calls"; then
        echo "FAIL: $script changed a service before refusing $case_name" >&2
        cat "$calls" >&2
        return 1
    fi
    if [ ! -f "$unit_config/systemd/user/corten-matrix.service" ]; then
        echo "FAIL: $script removed the user unit while refusing $case_name" >&2
        return 1
    fi
}

for script in scripts/install-linux.sh scripts/install-beeper-linux.sh; do
    script_id="${script//\//-}"
    run_refusal_case "$script" "${script_id}-user-bus-missing" false \
        "user service exists, but its systemd user manager is unreachable"
    run_refusal_case "$script" "${script_id}-custom-xdg-bus-missing" false \
        "user service exists, but its systemd user manager is unreachable" custom true
    run_refusal_case "$script" "${script_id}-default-fallback-with-xdg" false \
        "user service exists, but its systemd user manager is unreachable" default true
    run_refusal_case "$script" "${script_id}-both-scopes" true \
        "exists in both systemd scopes"
done

echo "Linux installer unit-scope guard tests passed."
