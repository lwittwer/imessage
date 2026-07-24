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
  --external-database-cleared    coordinate a selected PostgreSQL reset; after
                                 the fresh session export, reset pauses for you
                                 to clear the database and acknowledge that step

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
    0) DIRS=("$PRIMARY_DIR"); ACCOUNT_IDS=(0) ;;
    1) DIRS=("$SECOND_DIR"); ACCOUNT_IDS=(1) ;;
    all) DIRS=("$PRIMARY_DIR" "$SECOND_DIR"); ACCOUNT_IDS=(0 1) ;;
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
SELECTED_ACCOUNT_IDS=()
for i in "${!DIRS[@]}"; do
    if [ -d "${DIRS[$i]}" ]; then
        SELECTED+=("${DIRS[$i]}")
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
DB_TYPES=()
for dir in "${SELECTED[@]}"; do
    config="$dir/config.yaml"
    db_path="$dir/corten-matrix.db"
    db_type="sqlite3-fk-wal"
    if [ -f "$config" ]; then
        if ! db_type=$("$BINARY" reset-config-value "$config" database-type 2>/dev/null); then
            echo "ERROR: cannot read database type from $config." >&2
            echo "Nothing was stopped or deleted." >&2
            exit 1
        fi
    fi
    case "$db_type" in
        postgres)
            db_path=""
            ;;
        sqlite*)
            if [ -f "$config" ]; then
                if ! db_uri=$("$BINARY" reset-config-value "$config" database-uri 2>/dev/null); then
                    echo "ERROR: cannot read database URI from $config." >&2
                    echo "Nothing was stopped or deleted." >&2
                    exit 1
                fi
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
            ;;
        *)
            echo "ERROR: unsupported database type in $config: $db_type" >&2
            echo "Nothing was stopped or deleted." >&2
            exit 1
            ;;
    esac
    DB_TYPES+=("$db_type")
    DB_PATHS+=("$db_path")
done

# BEGIN RESET WHOAMI HELPER
whoami_has_bridge() {
    local bridge="$1"
    awk -v wanted="$bridge" 'NR > 1 && $1 == wanted { found=1 } END { exit(found ? 0 : 1) }'
}
# END RESET WHOAMI HELPER

# BEGIN RESET BRIDGE TARGET HELPER
bridge_target_is_unique() {
    local wanted="$1"
    local j
    for ((j=0; j<${#SELECTED_BRIDGES[@]}; j++)); do
        if [ -n "${SELECTED_BRIDGES[$j]}" ] && [ "${SELECTED_BRIDGES[$j]}" = "$wanted" ]; then
            return 1
        fi
    done
    return 0
}
# END RESET BRIDGE TARGET HELPER

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
SELECTED_BRIDGES=()
for i in "${!SELECTED[@]}"; do
    dir="${SELECTED[$i]}"
    policy=$(classify_remote_policy "$dir" "$KEEP_REMOTE")
    SELECTED_REMOTE_POLICY+=("$policy")
    case "$policy" in
        beeper)
            config="$dir/config.yaml"
            if ! bridge=$("$BINARY" reset-config-value "$config" appservice-id 2>/dev/null); then
                echo "ERROR: cannot determine the Beeper registration from $config." >&2
                echo "Nothing was stopped or deleted." >&2
                exit 1
            fi
            if ! bridge_target_is_unique "$bridge"; then
                echo "ERROR: selected accounts resolve to the same Beeper registration '$bridge'." >&2
                echo "Nothing was stopped or deleted." >&2
                exit 1
            fi
            SELECTED_BRIDGES+=("$bridge")
            SELECTED_DELETE_REMOTE+=(true)
            DELETE_REMOTE=true
            ;;
        self-hosted|local-only)
            SELECTED_BRIDGES+=("")
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
HAS_EXTERNAL_DB=false
for i in "${!SELECTED[@]}"; do
    if [ "${DB_TYPES[$i]}" = postgres ]; then
        HAS_EXTERNAL_DB=true
        if [ "$EXTERNAL_DB_CLEARED" != true ]; then
            config="${SELECTED[$i]}/config.yaml"
            echo "ERROR: $config uses PostgreSQL." >&2
            echo "Local file deletion cannot clear that external database." >&2
            echo "Back it up, then re-run with the coordinated reset flow:" >&2
            echo "  corten-matrix reset --account $ACCOUNT --external-database-cleared" >&2
            echo "Reset will stop the bridge, export its current session, then prompt" >&2
            echo "you to clear/recreate PostgreSQL before remote or local deletion." >&2
            echo "Nothing was stopped or deleted." >&2
            exit 1
        fi
    fi
done

# Verify Beeper credentials and connectivity before confirmations or stopping
# the bridge. The strict delete still runs after the daemon has stopped.
if [ "$DELETE_REMOTE" = true ]; then
    if ! WHOAMI_OUTPUT=$("$BINARY" bbctl whoami 2>/dev/null); then
        echo "ERROR: could not list Beeper registrations. Nothing was stopped or deleted." >&2
        echo "Resolve Beeper authentication/network access and retry." >&2
        exit 1
    fi
    for i in "${!SELECTED_BRIDGES[@]}"; do
        [ "${SELECTED_DELETE_REMOTE[$i]}" = true ] || continue
        bridge="${SELECTED_BRIDGES[$i]}"
        if ! printf '%s\n' "$WHOAMI_OUTPUT" | whoami_has_bridge "$bridge"; then
            echo "ERROR: selected config names Beeper registration '$bridge'," >&2
            echo "but bbctl whoami does not list that exact registration." >&2
            echo "Nothing was stopped or deleted." >&2
            exit 1
        fi
    done
fi

echo ""
echo "DANGER: this will stop the bridge. Planned actions:"
for i in "${!SELECTED[@]}"; do
    echo ""
    echo "  Account ${SELECTED_ACCOUNT_IDS[$i]}: ${SELECTED[$i]}"
    echo "    - DELETE local bridge database (including portal/backfill mappings) and logs"
    case "${SELECTED_REMOTE_POLICY[$i]}" in
        beeper)
            echo "    - DELETE Beeper bridge registration '${SELECTED_BRIDGES[$i]}'; its Matrix rooms may also be removed"
            echo "    - DELETE stale local bridge config so setup-beeper can regenerate it"
            echo "    - PRESERVE its database configuration for setup-beeper"
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

# Preserve the database stanza before invalidating the appservice
# credentials. setup-beeper merges this stanza into the freshly registered
# config and removes the backup only after a successful merge.
CONFIG_BACKUPS=()
SESSION_STATE_DIRS=()
SESSION_EXPORT_TOKENS=()
for i in "${!SELECTED[@]}"; do
    dir="${SELECTED[$i]}"
    config_backup=""
    if [ "${SELECTED_DELETE_REMOTE[$i]}" = true ]; then
        config_backup="$dir/config.reset-backup.yaml"
        config_backup_tmp="$config_backup.tmp.$BASHPID"
        if ! cp -p -- "$dir/config.yaml" "$config_backup_tmp" ||
            ! chmod 0600 "$config_backup_tmp" ||
            ! mv -f -- "$config_backup_tmp" "$config_backup"; then
            rm -f -- "$config_backup_tmp"
            echo "ERROR: could not preserve database configuration for $dir." >&2
            echo "The bridge was not stopped and nothing was deleted." >&2
            exit 1
        fi
    fi
    CONFIG_BACKUPS+=("$config_backup")

    if [ "$dir" = "$SECOND_DIR" ]; then
        session_state_dir="$dir/corten-matrix"
    else
        session_state_dir="$dir"
    fi
    SESSION_STATE_DIRS+=("$session_state_dir")
    export_token=""
    if [ "$DELETE_IMESSAGE_STATE" != true ]; then
        if ! mkdir -p "$session_state_dir"; then
            echo "ERROR: could not prepare session export directory $session_state_dir." >&2
            echo "The bridge was not stopped and nothing was deleted." >&2
            exit 1
        fi
        export_token="$BASHPID-$RANDOM-$i"
        request="$session_state_dir/.reset-session-export-request"
        ack="$session_state_dir/.reset-session-export-ack"
        request_tmp="$request.tmp.$BASHPID"
        rm -f -- "$request" "$ack" "$request_tmp"
        if ! (umask 077 && printf '%s\n' "$export_token" > "$request_tmp") ||
            ! mv -f -- "$request_tmp" "$request"; then
            rm -f -- "$request_tmp"
            echo "ERROR: could not request a fresh session export for $dir." >&2
            echo "The bridge was not stopped and nothing was deleted." >&2
            exit 1
        fi
    fi
    SESSION_EXPORT_TOKENS+=("$export_token")
done

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

# The bridge's final shutdown save must acknowledge the exact request written
# above, proving that both the database metadata save and atomic session.json
# export succeeded during this reset. An already-stopped bridge cannot
# acknowledge a stale export. Validate that fresh backup and its keystore before
# deleting the DB. This is intentionally after the daemon has stopped and before
# any local or remote deletion.
if [ "$DELETE_IMESSAGE_STATE" != true ]; then
    echo ""
    echo "Validating preserved Apple/iMessage session state..."
    for i in "${!SELECTED[@]}"; do
        dir="${SELECTED[$i]}"
        session_state_dir="${SESSION_STATE_DIRS[$i]}"
        export_token="${SESSION_EXPORT_TOKENS[$i]}"
        ack="$session_state_dir/.reset-session-export-ack"
        ack_token=""
        if [ -f "$ack" ]; then
            IFS= read -r ack_token < "$ack" || true
        fi
        if [ "$ack_token" != "$export_token" ]; then
            echo "ERROR: the bridge did not acknowledge a fresh session export for $dir." >&2
            echo "The bridge remains stopped, but no database, logs, config, remote" >&2
            echo "registration, or Apple/iMessage state was deleted." >&2
            echo "Start this version of the bridge successfully, then retry reset." >&2
            exit 1
        fi
        config="$dir/config.yaml"
        require_keychain=false
        if [ -f "$config" ]; then
            if ! cloudkit=$("$BINARY" reset-config-value "$config" network-cloudkit-backfill 2>/dev/null) ||
                ! backfill_source=$("$BINARY" reset-config-value "$config" network-backfill-source 2>/dev/null); then
                echo "ERROR: could not inspect restore requirements in $config." >&2
                echo "No database, logs, config, remote registration, or Apple/iMessage state was deleted." >&2
                exit 1
            fi
            if [ "$cloudkit" = "true" ] && [ "$backfill_source" != "chatdb" ]; then
                require_keychain=true
            fi
        fi
        session_xdg=$(dirname "$session_state_dir")
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

if [ "$HAS_EXTERNAL_DB" = true ]; then
    echo ""
    echo "The bridge is stopped and its fresh Apple/iMessage session export is valid."
    echo "Back up and clear/recreate each selected PostgreSQL database now."
    echo "No remote registration or local bridge state has been deleted yet."
    read -r -p "Type EXTERNAL DATABASE CLEARED after completing that step: " CONFIRM_EXTERNAL_DB
    if [ "$CONFIRM_EXTERNAL_DB" != "EXTERNAL DATABASE CLEARED" ]; then
        echo "Reset cancelled; the bridge remains stopped and no remote or local bridge state was deleted."
        exit 1
    fi
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
    rm -f -- "${SESSION_STATE_DIRS[$i]}/.reset-session-export-request" \
        "${SESSION_STATE_DIRS[$i]}/.reset-session-export-ack"
done

echo ""
echo "Bridge reset complete."
for i in "${!SELECTED[@]}"; do
    echo ""
    echo "Account ${SELECTED_ACCOUNT_IDS[$i]}: local database and logs deleted."
    if [ "${SELECTED_DELETE_REMOTE[$i]}" = true ]; then
        echo "  Beeper registration deletion completed; local config removed."
        if [ -n "${CONFIG_BACKUPS[$i]}" ]; then
            echo "  Database configuration preserved for setup-beeper."
        fi
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
