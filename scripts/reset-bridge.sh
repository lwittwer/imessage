#!/usr/bin/env bash
#
# Explicit bridge reset. Local state is always confirmed; Beeper registration
# deletion is a separate opt-in because it can remove remote Matrix rooms.
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
EXTERNAL_DB_CLEARED=false

usage() {
    cat <<'EOF'
Usage: corten-matrix reset [--account 0|1|all] [--delete-remote] [--external-database-cleared]

Deletes local bridge state only after an exact confirmation. By default, Matrix
rooms and Beeper registrations are left untouched.

  --account 0|1|all              reset one account or all configured accounts
  --delete-remote                Beeper only: also delete the selected bridge
                                 registration(s); requires a second confirmation
                                 and may remove their Matrix rooms
  --external-database-cleared    assert that every selected PostgreSQL database
                                 has already been backed up and cleared manually

For duplicate-DM recovery, install a binary containing the canonicalization fix
before resetting. See the README recovery section before using this command.
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

if [ "$DELETE_REMOTE" = true ]; then
    for dir in "${SELECTED[@]}"; do
        config="$dir/config.yaml"
        if [ ! -f "$config" ] || ! grep -Eqi '^[[:space:]]+domain:[[:space:]]+beeper\.com([[:space:]]|$)' "$config"; then
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
        if [ -f "$config" ] && grep -Eq '^[[:space:]]+type:[[:space:]]+postgres([[:space:]]|$)' "$config"; then
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
echo "DANGER: this will stop the bridge and permanently delete local state:"
for dir in "${SELECTED[@]}"; do
    echo "  - $dir"
done
echo ""
echo "This removes the bridge database, configuration, iMessage login/session,"
echo "backfill cache, and logs. You will need to run setup and log in again."
echo ""
if [ "$DELETE_REMOTE" = true ]; then
    echo "REMOTE DELETION REQUESTED: the selected Beeper registration(s) will also"
    echo "be deleted. This may remove ALL Matrix rooms owned by those registrations,"
    echo "not only duplicate DMs. This cannot be undone by restoring local files."
else
    echo "Matrix rooms and Beeper registrations will NOT be deleted. Old rooms will"
    echo "remain in Matrix, and newly rebuilt canonical rooms may coexist with them."
    echo "Verify the rebuilt rooms before manually leaving or archiving old duplicates."
fi
echo ""
read -r -p "Type RESET LOCAL STATE to continue: " CONFIRM_LOCAL
if [ "$CONFIRM_LOCAL" != "RESET LOCAL STATE" ]; then
    echo "Reset cancelled; nothing was changed."
    exit 1
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

if [ "$DELETE_REMOTE" = true ]; then
    echo ""
    for bridge in "${SELECTED_BRIDGES[@]}"; do
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
for dir in "${SELECTED[@]}"; do
    echo "Deleting local state: $dir"
    rm -rf -- "$dir"
done

echo ""
echo "Bridge reset complete."
if [ "$DELETE_REMOTE" = true ]; then
    echo "Remote Beeper registration deletion was requested and completed."
else
    echo "Remote Matrix rooms and bridge registrations were left untouched."
fi
echo "Install the fixed binary first, then run the matching setup command to rebuild."
