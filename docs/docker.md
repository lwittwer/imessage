# Docker

The bridge ships as a Docker image at `ghcr.io/lrhodin/imessage`. The image bundles the bridge binary (built with all rustpush patches applied via `make build`), `bbctl`, the existing install scripts, runtime dependencies, and the Apple Root CA. Updates are `docker compose pull && docker compose up -d`.

State lives on a bind mount at `~/.local/share/mautrix-imessage` — the same path the bare-Linux install uses. That makes migrating in either direction a matter of pointing the mount at the existing directory; no state copy.

## Quick start (new install)

```bash
curl -L https://raw.githubusercontent.com/lrhodin/imessage/master/docker-compose.example.yml -o docker-compose.yml
# Edit docker-compose.yml:
#   - Set BEEPER to "true" (Beeper) or "false" (self-hosted).
#   - Change the bind mount under volumes: to whatever host path suits
#     you — see "Host paths" below.
docker compose up -d
docker exec -it bridge imessage-setup
```

You don't need to `mkdir` the host bind-mount source. If it doesn't exist, Docker creates it on first `docker compose up` and the container chowns it to the bridge user automatically.

`imessage-setup` runs the same install script bare-Linux uses (`scripts/install-beeper-linux.sh` when `BEEPER=true`, `scripts/install-linux.sh` when `BEEPER=false`). Walk through the prompts — same UX as today. When the script finishes the bridge picks up `/data/config.yaml` on its next poll (within ~30s) and starts.

To flip toggles later (CardDAV, FaceTime, StatusKit, HEIC, transcoding, preferred handle):

```bash
docker exec -it bridge imessage-setup
```

The script re-runs against the existing config — same idempotent re-run pattern as bare-Linux.

## Migrating from a bare-Linux install

If you already have a working bare-Linux install at `~/.local/share/mautrix-imessage` and want to move to Docker without losing state, login, or the iCloud Keychain trust circle:

1. **Drop in compose, but don't start it yet.**
   ```bash
   curl -L https://raw.githubusercontent.com/lrhodin/imessage/master/docker-compose.example.yml -o docker-compose.yml
   ```

2. **Edit `docker-compose.yml`:**
   - Set `BEEPER` to `"true"` or `"false"` to match your existing install.
   - Set `MIGRATE` to `"true"`.
   - Uncomment the two rc-file mounts under `volumes:`:
     ```yaml
       - ~/.bashrc:/host/.bashrc
       - ~/.zshrc:/host/.zshrc
     ```
     These let the container strip the bare-Linux `start-imessage` / `stop-imessage` / `restart-imessage` / `imessage-log` aliases (which wrap `systemctl` and would no longer work).

3. **Start in the foreground for the migration prompt:**
   ```bash
   docker compose up
   ```
   The container detects `MIGRATE=true`, strips the managed-alias block from any bind-mounted rc file (auto-detects bash and zsh — whichever you have), and prints the host-side `systemctl` commands you need to run.

4. **In another terminal on the host, tear down systemd:**
   ```bash
   # systemd --user (most installs)
   systemctl --user stop mautrix-imessage 2>/dev/null
   systemctl --user disable mautrix-imessage 2>/dev/null
   rm -f ~/.config/systemd/user/mautrix-imessage.service
   systemctl --user daemon-reload

   # systemd system (only if you installed as root)
   sudo systemctl stop mautrix-imessage 2>/dev/null
   sudo systemctl disable mautrix-imessage 2>/dev/null
   sudo rm -f /etc/systemd/system/mautrix-imessage.service
   sudo systemctl daemon-reload
   ```

5. **Back in the foreground container, press Enter** at the "host cleanup done" prompt. The container writes `/data/.docker-migration-done` so the migration won't re-trigger on future restarts, then exec's the bridge against your existing state.

6. **Comment the rc-file mounts back out and flip `MIGRATE` to `"false"`** in `docker-compose.yml`. They're only needed for the one-shot migration.

7. **Restart detached:**
   ```bash
   docker compose down
   docker compose up -d
   ```

The bridge is now running against the same `config.yaml`, `session.json`, `trustedpeers.plist`, etc. No re-login, no re-registration with Beeper / your homeserver.

## Updating

```bash
docker compose pull
docker compose up -d
```

The image is published by GitHub Actions on manual dispatch. There's no auto-publish on every commit, so a new tag means someone explicitly built and pushed it.

## Optional: shell aliases

The image's `aliases` subcommand prints a host-side alias block (currently `imessage-setup` and `imessage-logs`) you can append to your shell rc file. The block is emitted only when `EMIT_ALIASES=true` is set in compose (default in the example).

```bash
docker exec bridge /entrypoint.sh aliases >> ~/.bashrc  # or ~/.zshrc
```

`restart-imessage` is not provided — `restart: unless-stopped` in compose makes it redundant, and the right way to restart is `docker compose restart bridge`.

## Host paths

The container only cares about `/data` internally — the host bind-mount source is your choice. Pick whatever makes sense for your platform:

| Platform | Typical host path |
|---|---|
| Standard Linux | `~/.local/share/mautrix-imessage` (matches bare-Linux install path) |
| UNRAID | `/mnt/user/appdata/mautrix-imessage` |
| Synology | `/volume1/docker/mautrix-imessage` |
| TrueNAS / ZFS | dataset of your choice |

Edit the `volumes:` line in `docker-compose.yml` to point at your path. If the directory doesn't exist on the host, Docker creates it on first `docker compose up`.

## UID / GID

The container starts as root just long enough to `chown /data` to the bridge user, then drops privileges via `gosu`. The long-lived bridge process is never root.

Defaults: UID 1000, GID 1000 (matches the typical first Linux user). Override via `PUID` / `PGID` in the compose `environment:` block when your host appdata directory is owned by a different UID — common on UNRAID (often `99:100`), Synology, or shared-server setups. Example:

```yaml
environment:
  PUID: "99"
  PGID: "100"
```

Check with `stat -c '%u:%g' /path/to/your/appdata` to see what UID/GID your data dir uses.

## Apple Silicon NAC relay

If your hardware key was extracted from an Apple Silicon Mac, the bridge inside the container fetches NAC validation data from a relay running on that Mac. The relay URL, bearer token, and TLS fingerprint are all embedded in the base64 hardware-key blob, so there's nothing extra to configure in compose — the container just needs network reachability to the relay's hostname/port.

For Docker on the same Mac, `host.docker.internal` resolves to the host. For Docker on a Linux server reaching a remote Mac, the key needs to have been extracted with a hostname/IP that's routable from the Linux server (LAN, VPN, or port-forwarded WAN).

## Out of scope

- **`backfill_source: chatdb`** doesn't work in Docker (macOS-only, Full Disk Access required). Use CloudKit backfill.
- **macOS Contacts framework** — same reason. Use the external CardDAV path if you want non-iCloud contacts.
- **`extract-key` / NAC relay GUIs** — those run on the user's Mac to mint the hardware key, not inside the bridge container.
