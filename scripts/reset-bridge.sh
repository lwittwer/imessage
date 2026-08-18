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
for arg in "$@"; do
    case "$arg" in
        -y|--yes) ASSUME_YES=1 ;;
        --delete-imessage-state) DELETE_IMESSAGE_STATE=1 ;;
    esac
done

STATE_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/corten-matrix"
BRIDGE_NAME="${BRIDGE_NAME:-sh-imessage}"
SERVICE_NAME="${SERVICE_NAME:-corten-matrix}"
UNAME_S=$(uname -s)

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

sleep 1
# Match the service process specifically (`corten-matrix bridge-all`), not any
# process whose command line contains "corten-matrix" — this script's parent
# is `corten-matrix reset`, so a broad match would always see itself still
# running and abort the reset.
if pgrep -f "corten-matrix bridge-all" >/dev/null 2>&1; then
    echo "ERROR: bridge process still running after stop" >&2
    exit 1
fi

# ── Delete server-side registration (cleans up Matrix rooms) ──
# bbctl is compiled into the corten-matrix binary (pkg/bbctl) and is invoked
# as `corten-matrix bbctl ...` — there is no standalone bbctl to locate.
# If the binary isn't usable, skip deregistration rather than aborting: the
# local cleanup is still useful, and a self-hosted install has no Beeper
# registration to delete in the first place.
echo ""
if [ -n "$BINARY" ] && [ -x "$BINARY" ]; then
    # Check whoami first: a registration the server has already dropped can
    # linger in bbctl whoami, and `bbctl delete` then fails with M_NOT_FOUND
    # (HTTP 404). Under set -e that aborts the reset with a confusing error,
    # even though there's nothing left to delete.
    if "$BINARY" bbctl whoami 2>/dev/null | grep -q "^[[:space:]]*$BRIDGE_NAME "; then
        echo "Deleting bridge registration from Beeper..."
        echo "(Answer the confirmation prompt below)"
        echo ""
        "$BINARY" bbctl delete "$BRIDGE_NAME" || \
            echo "⚠  Registration already absent on server — continuing with local cleanup."
    else
        echo "✓ No '$BRIDGE_NAME' registration on server — skipping delete."
    fi
else
    echo "⚠  corten-matrix binary not available — skipping Beeper deregistration."
fi

# ── Clear journal logs ───────────────────────────────────────
echo ""
echo "Clearing bridge journal logs..."
if [ "$UNAME_S" != "Darwin" ]; then
    # Same scope the stop above resolved — a system unit's journal is not in
    # the user journal, so `--user` here would silently clear nothing.
    ${SYSTEMCTL_SUDO:+$SYSTEMCTL_SUDO} journalctl ${SYSTEMCTL_SCOPE:+$SYSTEMCTL_SCOPE} --unit="$SERVICE_NAME" --rotate 2>/dev/null || true
    ${SYSTEMCTL_SUDO:+$SYSTEMCTL_SUDO} journalctl ${SYSTEMCTL_SCOPE:+$SYSTEMCTL_SCOPE} --unit="$SERVICE_NAME" --vacuum-time=1s 2>/dev/null || true
    echo "✓ Logs cleared"
else
    echo "  (macOS — logs managed by launchd, skipping)"
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
            "$STATE_DIR"/config.yaml.bak.* \
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
