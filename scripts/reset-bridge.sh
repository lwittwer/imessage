#!/usr/bin/env bash
#
# Explicit bridge reset. Apple/iMessage login state is preserved by default.
# Beeper registrations are rebuilt by default; local-only reset is opt-in.
#
# This script is embedded in corten-matrix and invoked by pkg/cli. The first
# four arguments are internal, resolved paths/identifiers supplied by Go.
#
set -euo pipefail

BINARY="$1"
PRIMARY_DIR="$2"
SECOND_DIR="$3"
BUNDLE_ID="$4"
shift 4

ACCOUNT="all"
ACCOUNT_SPECIFIED=false
KEEP_REMOTE=false
FORCE_DELETE_REMOTE=false
DELETE_REMOTE=false
DELETE_IMESSAGE_STATE=false
EXTERNAL_DB_CLEARED=false

# BEGIN RESET YAML HELPER
read_yaml_scalar() {
    local key="$1"
    local config="$2"
    local value
    value=$(awk -v wanted="$key:" '$1 == wanted { sub(/^[^:]*:[[:space:]]*/, ""); print; exit }' "$config")
    value=${value%%#*}
    value="${value#"${value%%[![:space:]]*}"}"
    value="${value%"${value##*[![:space:]]}"}"
    value=${value#\"}
    value=${value%\"}
    value=${value#\'}
    value=${value%\'}
    printf '%s' "$value"
}
# END RESET YAML HELPER

usage() {
    cat <<'EOF'
Usage: corten-matrix reset [--account 0|1|all] [--local-only] [--delete-imessage-state] [--external-database-cleared]

Deletes the local bridge database and logs only after an exact confirmation.
Apple/iMessage state is always preserved unless explicitly deleted. For Beeper
accounts, reset deletes and rebuilds the remote registration by default. For
self-hosted accounts, remote cleanup is unavailable and reset is local only.

  --account 0|1|all              reset one account or explicitly reset both;
                                 required when both accounts are configured
  --local-only, --keep-remote    keep Beeper registration(s) and Matrix rooms;
                                 only reset the local bridge database and logs
  --delete-remote                compatibility alias for the Beeper default
  --delete-imessage-state        also delete Apple/iMessage login, cryptographic
                                 keys, and session state; requires a separate
                                 confirmation and a fresh Apple login afterward
  --external-database-cleared    assert that every selected PostgreSQL database
                                 has already been backed up and cleared manually

For duplicate-DM recovery, install a binary containing the canonicalization fix
before resetting. Remote Beeper cleanup and Apple-state deletion are independent:
the former is the default, while the latter always requires its explicit flag.
See the README recovery section before using this command.
EOF
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --account)
            [ "$#" -ge 2 ] || { echo "ERROR: --account requires 0, 1, or all" >&2; exit 2; }
            ACCOUNT="$2"
            ACCOUNT_SPECIFIED=true
            shift 2
            ;;
        --delete-remote)
            FORCE_DELETE_REMOTE=true
            shift
            ;;
        --local-only|--keep-remote)
            KEEP_REMOTE=true
            shift
            ;;
        --delete-imessage-state)
            DELETE_IMESSAGE_STATE=true
            shift
            ;;
        --external-database-cleared)
            EXTERNAL_DB_CLEARED=true
            shift
            ;;
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
done

if [ "$KEEP_REMOTE" = true ] && [ "$FORCE_DELETE_REMOTE" = true ]; then
    echo "ERROR: --local-only/--keep-remote cannot be combined with --delete-remote" >&2
    exit 2
fi

if [ "$ACCOUNT_SPECIFIED" != true ]; then
    if [ -d "$PRIMARY_DIR" ] && [ -d "$SECOND_DIR" ]; then
        echo "ERROR: both bridge accounts are configured; choose --account 0, 1, or all." >&2
        echo "Nothing was stopped or deleted." >&2
        exit 2
    elif [ -d "$PRIMARY_DIR" ]; then
        ACCOUNT=0
    elif [ -d "$SECOND_DIR" ]; then
        ACCOUNT=1
    fi
fi

case "$ACCOUNT" in
    0) DIRS=("$PRIMARY_DIR"); BRIDGES=("sh-imessage"); ACCOUNT_IDS=(0) ;;
    1) DIRS=("$SECOND_DIR"); BRIDGES=("sh-imessage1"); ACCOUNT_IDS=(1) ;;
    all) DIRS=("$PRIMARY_DIR" "$SECOND_DIR"); BRIDGES=("sh-imessage" "sh-imessage1"); ACCOUNT_IDS=(0 1) ;;
    *) echo "ERROR: --account must be 0, 1, or all" >&2; exit 2 ;;
esac

# Refuse suspicious targets even if a caller bypasses the Go dispatcher. The
# parent may be a custom XDG_DATA_HOME, but account directory basenames are fixed.
for dir in "${DIRS[@]}"; do
    case "$dir" in
        /*) ;;
        *) echo "ERROR: refusing non-absolute reset target: $dir" >&2; exit 1 ;;
    esac
    case "$(basename "$dir")" in
        corten-matrix|corten-matrix-1) ;;
        *) echo "ERROR: refusing unsafe reset target: $dir" >&2; exit 1 ;;
    esac
    if [ "$(dirname "$dir")" = "/" ]; then
        echo "ERROR: refusing reset target directly under /: $dir" >&2
        exit 1
    fi
done

if [ ! -t 0 ]; then
    echo "ERROR: reset requires an interactive terminal; no non-interactive bypass is provided." >&2
    exit 1
fi

SELECTED=()
SELECTED_BRIDGES=()
SELECTED_ACCOUNT_IDS=()
for i in "${!DIRS[@]}"; do
    if [ -d "${DIRS[$i]}" ]; then
        SELECTED+=("${DIRS[$i]}")
        SELECTED_BRIDGES+=("${BRIDGES[$i]}")
        SELECTED_ACCOUNT_IDS+=("${ACCOUNT_IDS[$i]}")
    fi
done

if [ "${#SELECTED[@]}" -eq 0 ]; then
    echo "No local bridge state exists for account '$ACCOUNT'; nothing was changed."
    exit 0
fi

# Resolve each generated SQLite URI before stopping anything. Reset must never
# claim success while a custom database path still contains the old portal
# rows, and it must never delete a database outside the selected account dir.
DB_PATHS=()
for dir in "${SELECTED[@]}"; do
    config="$dir/config.yaml"
    db_path="$dir/corten-matrix.db"
    if [ -f "$config" ] && ! grep -Eq "^[[:space:]]+type:[[:space:]]+['\"]?postgres['\"]?([[:space:]]|$)" "$config"; then
        db_uri=$(read_yaml_scalar uri "$config")
        if [ -n "$db_uri" ]; then
            case "$db_uri" in
                file:*) db_path=${db_uri#file:}; db_path=${db_path%%\?*} ;;
                *)
                    echo "ERROR: unsupported local database URI in $config: $db_uri" >&2
                    echo "Nothing was stopped or deleted." >&2
                    exit 1
                    ;;
            esac
        fi
        case "$db_path" in
            /*) ;;
            *) db_path="$dir/$db_path" ;;
        esac
        db_parent=$(cd "$(dirname "$db_path")" 2>/dev/null && pwd -P) || {
            echo "ERROR: database parent directory does not exist: $(dirname "$db_path")" >&2
            echo "Nothing was stopped or deleted." >&2
            exit 1
        }
        db_path="$db_parent/$(basename "$db_path")"
        dir_real=$(cd "$dir" && pwd -P)
        case "$db_path" in
            "$dir_real"/*) ;;
            *)
                echo "ERROR: refusing SQLite database outside selected account directory: $db_path" >&2
                echo "Nothing was stopped or deleted." >&2
                exit 1
                ;;
        esac
    elif [ -f "$config" ]; then
        db_path=""
    fi
    DB_PATHS+=("$db_path")
done

# BEGIN RESET REMOTE POLICY HELPER
classify_remote_policy() {
    local dir="$1"
    local keep_remote="$2"
    local config="$dir/config.yaml"
    local kind
    if [ "$keep_remote" = true ]; then
        printf 'local-only'
        return
    fi
    if [ ! -f "$config" ]; then
        printf 'unknown'
        return
    fi
    if ! kind=$("$BINARY" reset-config-kind "$config" 2>/dev/null); then
        printf 'unknown'
    else
        case "$kind" in
            beeper|self-hosted) printf '%s' "$kind" ;;
            *) printf 'unknown' ;;
        esac
    fi
}
# END RESET REMOTE POLICY HELPER

# A normal Beeper reset rebuilds the registration as well as the local DB.
# Self-hosted configs are deliberately local-only because bbctl cannot manage
# their rooms or appservice registration.
SELECTED_DELETE_REMOTE=()
SELECTED_REMOTE_POLICY=()
for i in "${!SELECTED[@]}"; do
    dir="${SELECTED[$i]}"
    policy=$(classify_remote_policy "$dir" "$KEEP_REMOTE")
    SELECTED_REMOTE_POLICY+=("$policy")
    case "$policy" in
        beeper)
            SELECTED_DELETE_REMOTE+=(true)
            DELETE_REMOTE=true
            ;;
        self-hosted|local-only)
            SELECTED_DELETE_REMOTE+=(false)
            ;;
        *)
            echo "ERROR: cannot determine whether account ${SELECTED_ACCOUNT_IDS[$i]} is Beeper or self-hosted" >&2
            echo "from $dir/config.yaml. Nothing was stopped or deleted." >&2
            echo "Repair the config, or use --local-only to explicitly keep remote state." >&2
            exit 1
            ;;
    esac
done

if [ "$FORCE_DELETE_REMOTE" = true ]; then
    for i in "${!SELECTED_REMOTE_POLICY[@]}"; do
        if [ "${SELECTED_REMOTE_POLICY[$i]}" != beeper ]; then
            echo "ERROR: --delete-remote requires every selected account to be Beeper;" >&2
            echo "account ${SELECTED_ACCOUNT_IDS[$i]} is ${SELECTED_REMOTE_POLICY[$i]}. Nothing was stopped or deleted." >&2
            exit 1
        fi
    done
fi

# A local file wipe cannot clear PostgreSQL. Require the operator to perform
# and acknowledge that separate destructive step instead of claiming a clean
# rebuild while silently retaining all portal rows.
if [ "$EXTERNAL_DB_CLEARED" != true ]; then
    for dir in "${SELECTED[@]}"; do
        config="$dir/config.yaml"
        if [ -f "$config" ] && grep -Eq "^[[:space:]]+type:[[:space:]]+['\"]?postgres['\"]?([[:space:]]|$)" "$config"; then
            echo "ERROR: $config uses PostgreSQL." >&2
            echo "Local file deletion cannot clear that external database." >&2
            echo "Back it up and clear/recreate it manually, then re-run with:" >&2
            echo "  corten-matrix reset --account $ACCOUNT --external-database-cleared" >&2
            echo "Nothing was stopped or deleted." >&2
            exit 1
        fi
    done
fi

# Verify Beeper credentials and connectivity before confirmations or stopping
# the bridge. The strict delete still runs after the daemon has stopped.
if [ "$DELETE_REMOTE" = true ]; then
    if ! "$BINARY" bbctl whoami >/dev/null 2>&1; then
        echo "ERROR: could not list Beeper registrations. Nothing was stopped or deleted." >&2
        echo "Resolve Beeper authentication/network access and retry." >&2
        exit 1
    fi
fi

echo ""
echo "DANGER: this will stop the bridge. Planned actions:"
for i in "${!SELECTED[@]}"; do
    echo ""
    echo "  Account ${SELECTED_ACCOUNT_IDS[$i]}: ${SELECTED[$i]}"
    echo "    - DELETE local bridge database (including portal/backfill mappings) and logs"
    case "${SELECTED_REMOTE_POLICY[$i]}" in
        beeper)
            echo "    - DELETE Beeper bridge registration; its Matrix rooms may also be removed"
            echo "    - DELETE stale local bridge config so setup-beeper can regenerate it"
            ;;
        local-only)
            echo "    - KEEP remote bridge registration, Matrix rooms, and local config (--local-only)"
            ;;
        self-hosted)
            echo "    - KEEP self-hosted remote state and local config (automatic cleanup unavailable)"
            ;;
    esac
    if [ "$DELETE_IMESSAGE_STATE" = true ]; then
        echo "    - DELETE Apple/iMessage login, cryptographic keys, and session state"
    else
        echo "    - KEEP Apple/iMessage login, cryptographic keys, and session state"
    fi
done
if [ "$DELETE_REMOTE" = true ]; then
    echo ""
    echo "Deleting a Beeper registration may remove ALL Matrix rooms owned by it,"
    echo "not only duplicate DMs. This cannot be undone by restoring local files."
fi
if [ "$DELETE_IMESSAGE_STATE" = true ]; then
    echo ""
    echo "Apple state deletion requires a fresh login and may appear as a new device."
fi
echo ""
read -r -p "Type RESET BRIDGE DATA to continue: " CONFIRM_LOCAL
if [ "$CONFIRM_LOCAL" != "RESET BRIDGE DATA" ]; then
    echo "Reset cancelled; nothing was changed."
    exit 1
fi

if [ "$DELETE_IMESSAGE_STATE" = true ]; then
    echo ""
    read -r -p "Type DELETE IMESSAGE STATE to confirm Apple session deletion: " CONFIRM_IMESSAGE
    if [ "$CONFIRM_IMESSAGE" != "DELETE IMESSAGE STATE" ]; then
        echo "Reset cancelled; nothing was changed."
        exit 1
    fi
fi

if [ "$DELETE_REMOTE" = true ]; then
    echo ""
    read -r -p "Type DELETE BEEPER BRIDGE to confirm Beeper deletion: " CONFIRM_REMOTE
    if [ "$CONFIRM_REMOTE" != "DELETE BEEPER BRIDGE" ]; then
        echo "Reset cancelled; nothing was changed."
        exit 1
    fi
fi

# Stop only after all required confirmations have succeeded.
echo "Stopping bridge..."
UNAME_S=$(uname -s)
if [ "$UNAME_S" = "Darwin" ]; then
    launchctl bootout "gui/$(id -u)/$BUNDLE_ID" 2>/dev/null || \
        launchctl unload "$HOME/Library/LaunchAgents/$BUNDLE_ID.plist" 2>/dev/null || true
else
    if systemctl --user show-environment >/dev/null 2>&1; then
        systemctl --user stop corten-matrix 2>/dev/null || true
    elif [ "$(id -u)" = "0" ]; then
        systemctl stop corten-matrix 2>/dev/null || true
    else
        sudo systemctl stop corten-matrix 2>/dev/null || true
    fi
fi

# Never remove state while a bridge daemon still appears to be running. The
# parent `corten-matrix reset` process does not match these daemon arguments.
for attempt in 1 2 3 4 5; do
    if ! pgrep -f '(bridge-all| -c .*config\.yaml)' >/dev/null 2>&1; then
        break
    fi
    if [ "$attempt" -eq 5 ]; then
        echo "ERROR: a corten-matrix bridge process is still running; no state was deleted." >&2
        exit 1
    fi
    sleep 1
done

# The bridge's final shutdown save refreshes session.json from the latest DB
# metadata. Validate that backup and its keystore before deleting the DB. This
# is intentionally after the daemon has stopped and before any local or remote
# deletion. Account 1 uses its own directory as XDG_DATA_HOME, making its state
# a nested corten-matrix subtree; account 0 uses the parent of its data dir.
if [ "$DELETE_IMESSAGE_STATE" != true ]; then
    echo ""
    echo "Validating preserved Apple/iMessage session state..."
    for dir in "${SELECTED[@]}"; do
        config="$dir/config.yaml"
        require_keychain=false
        if [ -f "$config" ]; then
            cloudkit=$(read_yaml_scalar cloudkit_backfill "$config")
            backfill_source=$(read_yaml_scalar backfill_source "$config")
            if [ "$cloudkit" = "true" ] && [ "$backfill_source" != "chatdb" ]; then
                require_keychain=true
            fi
        fi
        if [ "$dir" = "$SECOND_DIR" ]; then
            session_xdg="$dir"
        else
            session_xdg=$(dirname "$dir")
        fi
        if [ "$require_keychain" = true ]; then
            restore_args=(check-restore)
        else
            restore_args=(check-restore --without-keychain)
        fi
        if ! (cd "$dir" && XDG_DATA_HOME="$session_xdg" "$BINARY" "${restore_args[@]}"); then
            echo "ERROR: preserved Apple/iMessage state for $dir is not safely restorable." >&2
            echo "The bridge remains stopped, but no database, logs, config, remote" >&2
            echo "registration, or Apple/iMessage state was deleted." >&2
            echo "Resolve the session backup problem, or explicitly opt in with" >&2
            echo "--delete-imessage-state if a fresh Apple login is intended." >&2
            exit 1
        fi
    done
fi

if [ "$DELETE_REMOTE" = true ]; then
    echo ""
    for i in "${!SELECTED_BRIDGES[@]}"; do
        [ "${SELECTED_DELETE_REMOTE[$i]}" = true ] || continue
        bridge="${SELECTED_BRIDGES[$i]}"
        echo "Converging Beeper registration '$bridge' to deleted state..."
        if ! "$BINARY" bbctl delete "$bridge"; then
            echo "ERROR: remote deletion failed; local state was NOT deleted." >&2
            echo "An earlier selected registration may already have been deleted." >&2
            echo "The bridge remains stopped. Resolve the error or run reset again with --local-only." >&2
            exit 1
        fi
    done
fi

echo ""
# Keep the filesystem mutation in a small argument-driven function so its
# preservation contract can be exercised against temporary directories.
# BEGIN RESET FILESYSTEM HELPER
delete_local_bridge_data() {
    local dir="$1"
    local db_path="$2"
    local delete_remote="$3"
    local delete_imessage_state="$4"

    echo "Deleting local bridge database and logs: $dir"
    if [ -n "$db_path" ]; then
        rm -f -- "$db_path" "$db_path-wal" "$db_path-shm"
    fi
    rm -rf -- "$dir/logs"
    rm -f -- "$dir/bridge.stdout.log" "$dir/bridge.stderr.log"

    if [ "$delete_remote" = true ]; then
        # The appservice token in this config became invalid when bbctl deleted
        # the registration. Remove it only after successful remote deletion so
        # setup-beeper can create a fresh registration safely.
        rm -f -- "$dir/config.yaml"
    fi

    if [ "$delete_imessage_state" = true ]; then
        echo "Deleting Apple/iMessage state as explicitly requested: $dir"
        # Primary-account state lives directly in the account directory. The
        # second account sets XDG_DATA_HOME to its account directory, making
        # its Apple state a nested corten-matrix subtree. Remove both possible
        # layouts only under the already validated account directory.
        rm -f -- "$dir/session.json" "$dir/identity.plist" \
            "$dir/trustedpeers.plist" "$dir/keystore.plist" \
            "$dir/facetime-state.plist" "$dir/passwords-state.plist" \
            "$dir/statuskit-state.plist" "$dir/statuskit-channel-dates.plist" \
            "$dir/statuskit-cloud-channel-map.plist" "$dir/sharedstreams-state.plist"
        rm -rf -- "$dir/state" "$dir/anisette" "$dir/corten-matrix"
    fi
}
# END RESET FILESYSTEM HELPER

for i in "${!SELECTED[@]}"; do
    dir="${SELECTED[$i]}"
    db_path="${DB_PATHS[$i]}"
    delete_local_bridge_data "$dir" "$db_path" "${SELECTED_DELETE_REMOTE[$i]}" "$DELETE_IMESSAGE_STATE"
done

echo ""
echo "Bridge reset complete."
for i in "${!SELECTED[@]}"; do
    echo ""
    echo "Account ${SELECTED_ACCOUNT_IDS[$i]}: local database and logs deleted."
    if [ "${SELECTED_DELETE_REMOTE[$i]}" = true ]; then
        echo "  Beeper registration deletion completed; local config removed."
        if [ "${SELECTED_ACCOUNT_IDS[$i]}" = 1 ]; then
            echo "  Next: run corten-matrix setup-beeper 1."
        else
            echo "  Next: run corten-matrix setup-beeper."
        fi
    else
        echo "  Remote state and local config preserved."
    fi
    if [ "$DELETE_IMESSAGE_STATE" = true ]; then
        echo "  Apple/iMessage login and session state deleted as requested."
    else
        echo "  Apple/iMessage login and session state preserved."
    fi
done
if [ "$DELETE_REMOTE" != true ] && [ "$DELETE_IMESSAGE_STATE" != true ]; then
    echo ""
    echo "Configuration and Apple/iMessage state are ready; run corten-matrix start."
elif [ "$DELETE_IMESSAGE_STATE" = true ]; then
    echo ""
    echo "Complete Apple login for each reset account before starting the bridge."
fi
