#!/bin/sh
# ============================================================================
# healthcheck — Docker HEALTHCHECK probe for the mautrix-imessage-v2 bridge.
#
# cmd_run (docker-entrypoint.sh) ends with `exec "$BIN" …`, so when the bridge
# is actually running it REPLACES PID 1. We probe PID 1's cmdline rather than a
# listening port, because the Beeper path uses appservice-websocket (no inbound
# port) while self-hosted listens on 29325 — the PID-1 check works for both and
# needs no extra tooling.
#
#   * PID 1 is the bridge binary            -> healthy (exit 0)
#   * config.yaml not written yet           -> unhealthy, but QUIET: the
#                                              container is legitimately idle
#                                              waiting for `imessage setup`, so
#                                              don't clutter the log.
#   * config exists but PID 1 is the shell  -> unhealthy AND log it: the bridge
#                                              should be running but isn't (e.g.
#                                              the root-prelude privilege-drop
#                                              loop when PUID=0). We append a
#                                              line to the same file `imessage
#                                              logs` tails, so the failure is
#                                              visible instead of the container
#                                              sitting at "Up" with a silent log.
# ============================================================================
set -eu

LOG=/data/logs/bridge.log
CONFIG=/data/config.yaml

# Healthy: the bridge binary is PID 1.
if grep -qa mautrix-imessage-v2 /proc/1/cmdline 2>/dev/null; then
    exit 0
fi

# Not running yet and never configured — nothing is wrong, stay quiet.
[ -f "$CONFIG" ] || exit 1

# Configured but the bridge isn't the running process. Surface it in the log
# (JSON line matching the bridge's file writer so log parsers don't choke).
mkdir -p "$(dirname "$LOG")" 2>/dev/null || true
printf '{"time":"%s","level":"error","logger":"healthcheck","message":"bridge process is not running (PID 1 is not mautrix-imessage-v2) — container is Up but the bridge never started; check PUID/PGID and the entrypoint privilege-drop loop"}\n' \
    "$(date -Is 2>/dev/null || date)" >> "$LOG" 2>/dev/null || true
exit 1
