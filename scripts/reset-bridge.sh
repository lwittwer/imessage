#!/usr/bin/env bash
#
# Bridge reset: delete the Beeper registration and rebuild disposable bridge
# state. Apple/iMessage login state is preserved by default.
#
# Usage: corten-matrix reset [--yes] [--delete-imessage-state]
#   Prompts for confirmation unless --yes is passed. Refuses to run
#   non-interactively without --yes, since it deletes the Beeper registration
#   and local bridge state.
#
# Invoked by pkg/cli RunManagement as:
#   reset-bridge.sh <corten-binary> <bundle-id> [user args...]
#
set -euo pipefail

BINARY="${1:-}"
[ $# -gt 0 ] && shift
BUNDLE_ID="${1:-com.lrhodin.corten-matrix}"
[ $# -gt 0 ] && shift

ASSUME_YES=0
DELETE_IMESSAGE_STATE=0
usage() {
    cat <<'EOF'
Usage: corten-matrix reset [--yes] [--delete-imessage-state]

Rebuild the primary Beeper registration and default SQLite bridge state while
preserving Apple/iMessage login state. This intentionally narrow reset does not
support second accounts, self-hosted homeservers, custom SQLite paths, or
PostgreSQL databases.

  -y, --yes                 confirm non-interactively
  --delete-imessage-state   also wipe Apple/iMessage login and cryptographic state
  -h, --help                show this help
EOF
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        -y|--yes) ASSUME_YES=1 ;;
        --delete-imessage-state) DELETE_IMESSAGE_STATE=1 ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "ERROR: unknown reset option: $1" >&2
            usage >&2
            exit 2
            ;;
    esac
    shift
done

STATE_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/corten-matrix"
CONFIG="$STATE_DIR/config.yaml"
DB_PATH="$STATE_DIR/corten-matrix.db"
BRIDGE_NAME="${BRIDGE_NAME:-sh-imessage}"
SERVICE_NAME="${SERVICE_NAME:-corten-matrix}"
UNAME_S=$(uname -s)

# Read one scalar from a top-level YAML section without reserializing the file.
# Generated configs use simple scalar values for all fields reset needs. Missing
# or ambiguous values fail closed below.
config_section_scalar() {
    awk -v section="$1" -v key="$2" '
        /^[^[:space:]#][^:]*:/ {
            top = $0
            sub(/:.*/, "", top)
            gsub(/[[:space:]]/, "", top)
            in_section = (top == section)
            next
        }
        in_section && $0 ~ "^[[:space:]]+" key "[[:space:]]*:[[:space:]]" {
            value = $0
            sub(/^[^:]*:[[:space:]]*/, "", value)
            sub(/[[:space:]]+#.*/, "", value)
            gsub(/^[[:space:]\"]+|[[:space:]\"]+$/, "", value)
            print value
            exit
        }
    ' "$CONFIG"
}

# Fail before confirmation or service stop when this narrow reset cannot safely
# identify and remove the configured local database.
if [ ! -f "$CONFIG" ]; then
    echo "ERROR: reset requires the primary account config at $CONFIG." >&2
    echo "No service or local state was changed." >&2
    exit 1
fi
DB_TYPE=$(config_section_scalar database type)
DB_URI=$(config_section_scalar database uri)
HOMESERVER_ADDRESS=$(config_section_scalar homeserver address)
HOMESERVER_DOMAIN=$(config_section_scalar homeserver domain)
if [ -z "$DB_TYPE" ] || [ -z "$DB_URI" ]; then
    echo "ERROR: could not read the configured database type and URI from $CONFIG." >&2
    echo "No service or local state was changed." >&2
    exit 1
fi
case "$DB_TYPE" in
    sqlite*) ;;
    postgres)
        echo "ERROR: reset does not coordinate PostgreSQL databases." >&2
        echo "No service or local state was changed." >&2
        exit 1
        ;;
    *)
        echo "ERROR: unsupported database type '$DB_TYPE'; no service or local state was changed." >&2
        exit 1
        ;;
esac
case "$DB_URI" in
    file:*) CONFIGURED_DB_PATH=${DB_URI#file:}; CONFIGURED_DB_PATH=${CONFIGURED_DB_PATH%%\?*} ;;
    *)
        echo "ERROR: unsupported SQLite URI '$DB_URI'; no service or local state was changed." >&2
        exit 1
        ;;
esac
case "$CONFIGURED_DB_PATH" in
    "$DB_PATH"|corten-matrix.db) ;;
    *)
        echo "ERROR: reset only supports the default SQLite database at $DB_PATH." >&2
        echo "Configured database is $CONFIGURED_DB_PATH; no service or local state was changed." >&2
        exit 1
        ;;
esac
case "$HOMESERVER_DOMAIN" in
    beeper.com|beeper.local) ;;
    *)
        echo "ERROR: reset supports Beeper-generated configs only; homeserver domain is '$HOMESERVER_DOMAIN'." >&2
        echo "Self-hosted installs are out of scope; no service or local state was changed." >&2
        exit 1
        ;;
esac
case "$HOMESERVER_ADDRESS" in
    https://matrix.beeper.com/*) ;;
    *)
        echo "ERROR: reset supports Beeper-generated configs only; homeserver address is not Beeper." >&2
        echo "No service or local state was changed." >&2
        exit 1
        ;;
esac

CLOUDKIT_BACKFILL=$(config_section_scalar network cloudkit_backfill)
BACKFILL_SOURCE=$(config_section_scalar network backfill_source)
REQUIRE_KEYCHAIN=0
NORMALIZED_CLOUDKIT_BACKFILL=$(printf '%s' "$CLOUDKIT_BACKFILL" | tr '[:upper:]' '[:lower:]')
case "$NORMALIZED_CLOUDKIT_BACKFILL" in
    true)
        [ "$BACKFILL_SOURCE" = "chatdb" ] || REQUIRE_KEYCHAIN=1
        ;;
    false) ;;
    *)
        echo "ERROR: invalid cloudkit_backfill value '$CLOUDKIT_BACKFILL' in $CONFIG." >&2
        echo "No service or local state was changed." >&2
        exit 1
        ;;
esac

if [ -z "$BINARY" ] || [ ! -x "$BINARY" ]; then
    echo "ERROR: corten-matrix binary is not available; no service or local state was changed." >&2
    exit 1
fi
if ! command -v pgrep >/dev/null 2>&1; then
    echo "ERROR: pgrep is required to prove that the bridge stopped." >&2
    echo "No service or local state was changed." >&2
    exit 1
fi

# This command is intentionally Beeper-only. Verify authentication before the
# confirmation and stop so self-hosted or unauthenticated installs fail without
# a lifecycle change. Revalidate again at the deletion boundary below.
if ! "$BINARY" bbctl whoami >/dev/null 2>&1; then
    echo "ERROR: Beeper registration preflight failed." >&2
    echo "This reset supports authenticated Beeper installs only; self-hosted installs are out of scope." >&2
    echo "No service or local state was changed." >&2
    exit 1
fi

# ── Confirm BEFORE touching anything ─────────────────────────
# The normal reset deletes the Beeper registration (tearing down the Matrix
# rooms) and known disposable bridge files, while retaining Apple/iMessage
# login state. The explicit Apple-state flag adds a second exact confirmation.
if [ "$ASSUME_YES" -ne 1 ]; then
    if [ ! -t 0 ]; then
        echo "ERROR: refusing to reset non-interactively. Re-run with --yes if you mean it." >&2
        exit 1
    fi
    echo "This will:"
    echo "  • stop the bridge"
    echo "  • delete the '$BRIDGE_NAME' registration from Beeper (removes the Matrix rooms)"
    echo "  • delete the local bridge config, database, and logs"
    if [ "$DELETE_IMESSAGE_STATE" -eq 1 ]; then
        echo "  • also wipe Apple/iMessage login and cryptographic state"
    else
        echo "  • preserve Apple/iMessage login and other non-bridge state"
    fi
    echo ""
    if [ "$DELETE_IMESSAGE_STATE" -eq 1 ]; then
        echo "You will need to re-login (2FA) afterwards. This cannot be undone."
    else
        echo "Your saved Apple/iMessage login will be reused by setup-beeper."
    fi
    echo ""
    read -r -p "Type 'reset' to confirm: " reply || reply=""
    # Tolerate a trailing CR (CRLF terminals, pty wrappers) and stray spaces;
    # anything else still has to be exactly "reset".
    reply=$(printf '%s' "$reply" | tr -d '[:space:]')
    if [ "$reply" != "reset" ]; then
        echo "Aborted — nothing was changed."
        exit 1
    fi

    if [ "$DELETE_IMESSAGE_STATE" -eq 1 ]; then
        echo ""
        echo "Apple/iMessage login, cryptographic keys, and session state will be deleted."
        read -r -p "Type 'DELETE IMESSAGE STATE' to confirm: " imessage_reply || imessage_reply=""
        # Trim only surrounding whitespace so the words and their spacing are
        # part of the exact confirmation. Command substitution also removes a
        # trailing newline supplied by an interactive terminal.
        imessage_reply=$(printf '%s' "$imessage_reply" |
            sed 's/^[[:space:]]*//; s/[[:space:]]*$//')
        if [ "$imessage_reply" != "DELETE IMESSAGE STATE" ]; then
            echo "Aborted — nothing was changed."
            exit 1
        fi
    fi
fi

# When preserving Apple state, a running bridge must atomically replace
# session.json during shutdown. Capture the inode immediately before stopping,
# after all interactive waits. Comparing inodes proves that the final save
# completed even when the serialized state is byte-for-byte unchanged. The
# explicitly authorized full-wipe path does not need a restorable export.
BRIDGE_WAS_RUNNING=0
SESSION_INODE_BEFORE=""
if pgrep -f "corten-matrix bridge-all" >/dev/null 2>&1; then
    BRIDGE_WAS_RUNNING=1
    if [ "$DELETE_IMESSAGE_STATE" -ne 1 ] && [ -f "$STATE_DIR/session.json" ]; then
        SESSION_INODE_BEFORE=$(ls -di "$STATE_DIR/session.json" | awk '{print $1}')
    fi
else
    PGR_STATUS=$?
    if [ "$PGR_STATUS" -ne 1 ]; then
        echo "ERROR: pgrep failed during reset preflight (status $PGR_STATUS)." >&2
        echo "No service or local state was changed." >&2
        exit 1
    fi
fi

# ── Stop the bridge ──────────────────────────────────────────
echo "Stopping bridge..."
SYSTEMCTL_SCOPE=""   # "--user", or "" for the system manager
SYSTEMCTL_SUDO=""    # "sudo" when driving the system manager as non-root
if [ "$UNAME_S" = "Darwin" ]; then
    launchctl unload "$HOME/Library/LaunchAgents/$BUNDLE_ID.plist" 2>/dev/null || true
else
    # Pick the scope by where the unit actually IS, not by which bus answers.
    # `systemctl --user` succeeds whenever a user session bus is reachable —
    # even when the unit is a SYSTEM unit — so an unconditional `--user stop`
    # silently no-ops on a bridge installed via `sudo corten-matrix setup`.
    if systemctl --user cat "$SERVICE_NAME.service" >/dev/null 2>&1; then
        SYSTEMCTL_SCOPE="--user"
    elif systemctl cat "$SERVICE_NAME.service" >/dev/null 2>&1; then
        [ "$(id -u)" = "0" ] || SYSTEMCTL_SUDO="sudo"
    else
        # Nothing installed in either scope; the stop is a no-op either way.
        SYSTEMCTL_SCOPE="--user"
    fi
    ${SYSTEMCTL_SUDO:+$SYSTEMCTL_SUDO} systemctl ${SYSTEMCTL_SCOPE:+$SYSTEMCTL_SCOPE} stop "$SERVICE_NAME" 2>/dev/null || true
fi

# Match the service process specifically (`corten-matrix bridge-all`), not any
# process whose command line contains "corten-matrix" — this script's parent
# is `corten-matrix reset`, so a broad match would always see itself still
# running and abort the reset.
BRIDGE_STOPPED=0
attempt=1
while [ "$attempt" -le 35 ]; do
    if pgrep -f "corten-matrix bridge-all" >/dev/null 2>&1; then
        PGR_STATUS=0
    else
        PGR_STATUS=$?
    fi
    case "$PGR_STATUS" in
        1)
            BRIDGE_STOPPED=1
            break
            ;;
        0)
            [ "$attempt" -lt 35 ] && sleep 1
            attempt=$((attempt + 1))
            ;;
        *)
            echo "ERROR: pgrep failed while verifying bridge shutdown (status $PGR_STATUS)." >&2
            echo "The bridge may be stopped, but no registration or local state was deleted." >&2
            exit 1
            ;;
    esac
done
if [ "$BRIDGE_STOPPED" -ne 1 ]; then
    echo "ERROR: bridge process still running after 35 seconds" >&2
    exit 1
fi
if [ "$DELETE_IMESSAGE_STATE" -ne 1 ] && [ "$BRIDGE_WAS_RUNNING" -eq 1 ] && [ -n "$SESSION_INODE_BEFORE" ]; then
    if [ ! -f "$STATE_DIR/session.json" ]; then
        echo "ERROR: final shutdown did not preserve session.json." >&2
        echo "No registration or local state was deleted." >&2
        exit 1
    fi
    SESSION_INODE_AFTER=$(ls -di "$STATE_DIR/session.json" | awk '{print $1}')
    if [ "$SESSION_INODE_AFTER" = "$SESSION_INODE_BEFORE" ]; then
        echo "ERROR: session.json was not refreshed during final bridge shutdown." >&2
        echo "No registration or local state was deleted." >&2
        exit 1
    fi
    SESSION_ACK="$STATE_DIR/.session-save-ok"
    if [ ! -f "$SESSION_ACK" ]; then
        echo "ERROR: final session save was not durably acknowledged." >&2
        echo "No registration or local state was deleted." >&2
        exit 1
    fi
    SESSION_ACK_INODE=$(ls -di "$SESSION_ACK" | awk '{print $1}')
    if [ "$SESSION_ACK_INODE" != "$SESSION_INODE_AFTER" ]; then
        echo "ERROR: final session save acknowledgement does not match session.json." >&2
        echo "No registration or local state was deleted." >&2
        exit 1
    fi
fi

# A normal reset deletes the bridge database, so the saved Apple session must
# already be independently restorable. The bridge refreshes session.json during
# shutdown; validate that file against the keystore before deleting either the
# remote registration or any local bridge state. The explicit full-wipe path
# deliberately skips this check because it requires a new Apple login.
if [ "$DELETE_IMESSAGE_STATE" -ne 1 ]; then
    if [ -f "$STATE_DIR/session.json" ]; then
        echo "Validating preserved Apple/iMessage session state..."
        RESTORE_ARGS=(check-restore)
        if [ "$REQUIRE_KEYCHAIN" -eq 1 ]; then
            RESTORE_ARGS+=(--require-keychain)
        fi
        if ! "$BINARY" "${RESTORE_ARGS[@]}"; then
            echo "ERROR: preserved Apple/iMessage session state is not safely restorable." >&2
            echo "The bridge remains stopped, but no registration or local state was deleted." >&2
            echo "Repair the saved session, or use --delete-imessage-state only if a fresh Apple login is acceptable." >&2
            exit 1
        fi
    elif [ -f "$DB_PATH" ]; then
        if ! command -v sqlite3 >/dev/null 2>&1; then
            echo "ERROR: sqlite3 is required to verify that the bridge has never logged in." >&2
            echo "The bridge remains stopped, but no registration or local state was deleted." >&2
            exit 1
        fi
        if ! LOGIN_COUNT=$(sqlite3 "$DB_PATH" "SELECT count(*) FROM user_login;" 2>/dev/null); then
            echo "ERROR: could not verify login state in $DB_PATH." >&2
            echo "The bridge remains stopped, but no registration or local state was deleted." >&2
            exit 1
        fi
        case "$LOGIN_COUNT" in
            0) echo "No saved Apple/iMessage login exists; continuing with bridge-state reset." ;;
            *)
                echo "ERROR: the database contains an Apple login but session.json is missing." >&2
                echo "The bridge remains stopped, but no registration or local state was deleted." >&2
                echo "Recover session.json, or use --delete-imessage-state only if a fresh Apple login is acceptable." >&2
                exit 1
                ;;
        esac
    else
        echo "No saved Apple/iMessage login exists; continuing with bridge-state reset."
    fi
fi

# ── Delete server-side registration (cleans up Matrix rooms) ──
# bbctl is compiled into the corten-matrix binary (pkg/bbctl) and is invoked
# as `corten-matrix bbctl ...` — there is no standalone bbctl to locate.
# The reset is invoked through the corten-matrix binary. If that binary cannot
# authenticate to Beeper, fail before local cleanup rather than guessing that
# the registration is absent.
echo ""
# Check whoami first: a registration the server has already dropped can linger
# there while `bbctl delete` returns M_NOT_FOUND. Revalidation distinguishes
# that idempotent case from authentication and connectivity failures.
if ! WHOAMI_OUTPUT=$("$BINARY" bbctl whoami 2>/dev/null); then
    echo "ERROR: could not revalidate the Beeper registration; local state was not deleted." >&2
    exit 1
fi
if printf '%s\n' "$WHOAMI_OUTPUT" | grep -q "^[[:space:]]*$BRIDGE_NAME "; then
    echo "Deleting and verifying the Beeper registration (this can take up to a minute)..."
    echo ""
    if ! "$BINARY" bbctl delete "$BRIDGE_NAME"; then
        echo "ERROR: Beeper registration deletion failed; local state was not deleted." >&2
        echo "Resolve the Beeper error and run reset again." >&2
        exit 1
    fi
else
    echo "✓ No '$BRIDGE_NAME' registration on server — skipping delete."
fi

# ── Remove local bridge state ─────────────────────────────────
echo ""
if [ "$DELETE_IMESSAGE_STATE" -eq 1 ]; then
    # This is the exceptional, explicit full wipe. bridge-manager/ is excluded
    # for compatibility with pre-2026-06 installs that still have a standalone
    # bbctl (and its Beeper credentials); current installs never create it.
    echo "Wiping all state in $STATE_DIR/ ..."
    if [ -d "$STATE_DIR" ]; then
        find "$STATE_DIR" -maxdepth 1 -not -name bridge-manager -not -path "$STATE_DIR" -exec rm -rf {} +

        # Verify
        REMAINING=$(find "$STATE_DIR" -maxdepth 1 -not -name bridge-manager -not -path "$STATE_DIR" | wc -l)
        if [ "$REMAINING" -ne 0 ]; then
            echo "ERROR: state directory not fully cleaned:" >&2
            ls -la "$STATE_DIR/"
            exit 1
        fi
    else
        echo "  (no state directory at $STATE_DIR — nothing to wipe)"
    fi
else
    # Keep this list deliberately explicit. In particular, do not enumerate
    # or remove unknown files: Apple/iMessage state and future state files must
    # survive the default reset.
    echo "Removing disposable bridge state from $STATE_DIR/ ..."
    if [ -d "$STATE_DIR" ]; then
        rm -f -- \
            "$STATE_DIR/config.yaml" \
            "$STATE_DIR/config.reset-backup.yaml" \
            "$STATE_DIR/.config.reset-new.yaml" \
            "$STATE_DIR/corten-matrix.db" \
            "$STATE_DIR/corten-matrix.db-wal" \
            "$STATE_DIR/corten-matrix.db-shm" \
            "$STATE_DIR/corten-matrix.db-journal" \
            "$STATE_DIR/bridge.stdout.log" \
            "$STATE_DIR/bridge.stderr.log"
        rm -rf -- "$STATE_DIR/logs"
    else
        echo "  (no state directory at $STATE_DIR — nothing to clean)"
    fi
fi

echo ""
echo "✓ Bridge reset complete."
if [ "$DELETE_IMESSAGE_STATE" -eq 1 ]; then
    echo "  All local state wiped — you will need to re-login (2FA)."
else
    echo "  Apple/iMessage login and other non-bridge state preserved."
fi
echo ""
echo "  Run 'corten-matrix setup-beeper' to re-register and start the bridge."
