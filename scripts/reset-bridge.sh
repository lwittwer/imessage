#!/usr/bin/env bash
#
# Explicit bridge reset. Apple/iMessage login state is preserved by default.
# Deleting it and deleting a Beeper registration are independent opt-ins.
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
Usage: corten-matrix reset [--account 0|1|all] [--delete-remote] [--delete-imessage-state] [--external-database-cleared]

Deletes the local bridge database and logs only after an exact confirmation.
By default, configuration, Apple/iMessage login state, Matrix rooms, and Beeper
registrations are preserved.

  --account 0|1|all              reset one account or all configured accounts
  --delete-remote                Beeper only: also delete the selected bridge
                                 registration(s); requires a second confirmation
                                 and may remove their Matrix rooms
  --delete-imessage-state        also delete Apple/iMessage login, cryptographic
                                 keys, and session state; requires a separate
                                 confirmation and a fresh Apple login afterward
  --external-database-cleared    assert that every selected PostgreSQL database
                                 has already been backed up and cleared manually

For duplicate-DM recovery, install a binary containing the canonicalization fix
before resetting. The two deletion flags are independent. See the README
recovery section before using this command.
EOF
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --account)
            [ "$#" -ge 2 ] || { echo "ERROR: --account requires 0, 1, or all" >&2; exit 2; }
            ACCOUNT="$2"
            shift 2
            ;;
        --delete-remote)
            DELETE_REMOTE=true
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

case "$ACCOUNT" in
    0) DIRS=("$PRIMARY_DIR"); BRIDGES=("sh-imessage") ;;
    1) DIRS=("$SECOND_DIR"); BRIDGES=("sh-imessage1") ;;
    all) DIRS=("$PRIMARY_DIR" "$SECOND_DIR"); BRIDGES=("sh-imessage" "sh-imessage1") ;;
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
for i in "${!DIRS[@]}"; do
    if [ -d "${DIRS[$i]}" ]; then
        SELECTED+=("${DIRS[$i]}")
        SELECTED_BRIDGES+=("${BRIDGES[$i]}")
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

if [ "$DELETE_REMOTE" = true ]; then
    for dir in "${SELECTED[@]}"; do
        config="$dir/config.yaml"
        if [ ! -f "$config" ] || ! grep -Eqi "^[[:space:]]+domain:[[:space:]]+['\"]?beeper\\.com['\"]?([[:space:]]|$)" "$config"; then
            echo "ERROR: --delete-remote is only available for a selected Beeper account" >&2
            echo "with an identifiable local config. Nothing was stopped or deleted." >&2
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

echo ""
echo "DANGER: this will stop the bridge and permanently delete its database and logs:"
for dir in "${SELECTED[@]}"; do
    echo "  - $dir"
done
echo ""
echo "This removes the bridge database (including backfill/portal mappings) and logs."
echo "Bridge configuration will be preserved."
if [ "$DELETE_IMESSAGE_STATE" = true ]; then
    echo ""
    echo "IMESSAGE STATE DELETION REQUESTED: Apple/iMessage login, cryptographic"
    echo "keys, trusted-peers data, and session state will also be deleted. A fresh"
    echo "Apple login will be required, and Apple may treat it as a new device."
else
    echo "Apple/iMessage login, cryptographic keys, trusted-peers data, and session"
    echo "state will be preserved so setup can restore the existing Apple session."
fi
echo ""
if [ "$DELETE_REMOTE" = true ]; then
    echo "REMOTE DELETION REQUESTED: the selected Beeper registration(s) will also"
    echo "be deleted. This may remove ALL Matrix rooms owned by those registrations,"
    echo "not only duplicate DMs. This cannot be undone by restoring local files."
    echo "After remote deletion succeeds, its now-stale local config will be removed"
    echo "so setup can create a new registration. Apple/iMessage state is independent."
else
    echo "Matrix rooms and Beeper registrations will NOT be deleted. Old rooms will"
    echo "remain in Matrix, and newly rebuilt canonical rooms may coexist with them."
    echo "Verify the rebuilt rooms before manually leaving or archiving old duplicates."
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
    read -r -p "Type DELETE MATRIX ROOMS to confirm remote deletion: " CONFIRM_REMOTE
    if [ "$CONFIRM_REMOTE" != "DELETE MATRIX ROOMS" ]; then
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
    if ! remote_listing=$("$BINARY" bbctl whoami 2>&1); then
        echo "ERROR: could not list Beeper registrations; local state was NOT deleted." >&2
        echo "The bridge remains stopped. Resolve Beeper authentication/network access and retry." >&2
        exit 1
    fi
    for bridge in "${SELECTED_BRIDGES[@]}"; do
        if ! printf '%s\n' "$remote_listing" | grep -Eq "^[[:space:]]*$bridge([[:space:]]|$)"; then
            echo "Beeper registration '$bridge' is already absent; continuing."
            continue
        fi
        echo "Deleting Beeper registration '$bridge'..."
        if ! "$BINARY" bbctl delete "$bridge"; then
            echo "ERROR: remote deletion failed; local state was NOT deleted." >&2
            echo "An earlier selected registration may already have been deleted." >&2
            echo "The bridge remains stopped. Resolve the error or run reset again without --delete-remote." >&2
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
    delete_local_bridge_data "$dir" "$db_path" "$DELETE_REMOTE" "$DELETE_IMESSAGE_STATE"
done

echo ""
echo "Bridge reset complete."
if [ "$DELETE_IMESSAGE_STATE" = true ]; then
    echo "Apple/iMessage login and session state were deleted as requested."
else
    echo "Apple/iMessage login and session state were preserved."
fi
if [ "$DELETE_REMOTE" = true ]; then
    echo "Remote Beeper registration deletion was requested and completed."
else
    echo "Remote Matrix rooms and bridge registrations were left untouched."
fi
if [ "$DELETE_REMOTE" = true ]; then
    echo "Run the matching setup-beeper command to create a new registration."
elif [ "$DELETE_IMESSAGE_STATE" = true ]; then
    echo "Run corten-matrix login for each reset account, then start the bridge."
else
    echo "Configuration and Apple/iMessage state are ready; run corten-matrix start."
fi
