# Corten-Matrix

A Matrix–iMessage bridge built on [rustpush](https://github.com/OpenBubbles/rustpush)
and [bridgev2](https://mau.fi/blog/megabridge-twilio/). Send and receive iMessages
from any Matrix client through a direct connection to Apple's IDS/APNs services.

Supports messages, attachments, reactions, edits, unsends, typing indicators,
read receipts, groups, SMS forwarding, contacts, FaceTime notices, Focus status,
and iCloud Shared Albums.

Download a self-contained `corten-matrix` binary from
[Releases](https://github.com/lrhodin/corten-matrix/releases). It includes the
bridge and management CLI. macOS uses native validation; Linux uses a hardware
key extracted once from a Mac, with no Mac or relay needed at runtime.
[Source builds](#build-from-source-macos) are macOS-only. Docker images are not
currently maintained.

**Disable Contact Key Verification before login** — see [Troubleshooting](#troubleshooting).
If migrating from the old source-build/relay distribution, read the next section;
current binary users can follow [Updating](#updating).

## Upgrading to binary releases

Legacy source-build/relay installations require a fresh binary install and
backfill; their database is not carried forward. Follow the Quick Start for your
platform. Linux users must also re-extract their hardware key with the current
tool below: old relay-era keys are incompatible.

## Quick Start (macOS)

Requires macOS 13+ and an iCloud sign-in on the host Mac. No hardware-key
extraction is needed.

1. Download the universal macOS binary from [Releases](https://github.com/lrhodin/corten-matrix/releases)
   and rename it to `corten-matrix`.
2. Make it executable and run one setup command:

   ```bash
   chmod +x corten-matrix
   ./corten-matrix setup-beeper   # Beeper
   # or: ./corten-matrix setup    # self-hosted homeserver
   ```

Setup installs dependencies, prompts for configuration and Apple login, and
starts a LaunchAgent. For self-hosted Matrix, it provides the appservice
registration to add to `homeserver.yaml`. It also offers a `/usr/local/bin`
symlink so subsequent commands work without `./`.

## Quick Start (Linux)

Requires Ubuntu 22.04+ or equivalent, plus access to an Intel or Apple Silicon
Mac for the one-time extraction. Setup installs runtime dependencies through
apt, dnf, pacman, zypper, or apk. Nothing is compiled locally.

Containers are supported; see [Linux service scope](#linux).

### Step 1: Extract hardware key (one-time, on a Mac)

Use the current extractor; tools from before the binary switch are incompatible.
Both options produce the same base64 key on Intel and Apple Silicon Macs.

- **GUI:** download [ExtractKey.app.zip](https://github.com/lrhodin/corten-matrix/blob/d9fed308b33a03019fd4273a6921a0a5bf818564/tools/ExtractKey.app.zip),
  unzip, launch, then Copy or Save the key.
- **CLI:** download [extract-key-cli.zip](https://github.com/lrhodin/corten-matrix/blob/d9fed308b33a03019fd4273a6921a0a5bf818564/tools/extract-key-cli.zip),
  unzip, then run `chmod +x extract-key` and `./extract-key`.

The GUI app is ad-hoc signed, so Gatekeeper may block its first launch. On
macOS 13+, launch it once, then use **System Settings → Privacy & Security →
Open Anyway**. On macOS 10.15–12, right-click the app and choose **Open**.
Alternatively, run `xattr -cr <AppName>.app` before launching.

Save the key for login on Linux. The Mac needs no ongoing process or relay.

### Step 2: Install the bridge (on Linux)

Download the `linux-amd64` or `linux-arm64` binary from
[Releases](https://github.com/lrhodin/corten-matrix/releases), rename it to
`corten-matrix`, then run:

```bash
chmod +x corten-matrix
./corten-matrix setup-beeper   # Beeper
# or: ./corten-matrix setup    # self-hosted homeserver
```

### Step 3: Login

Setup runs [login](#login) when no saved session exists. Paste the extracted
hardware key, then enter your Apple ID, password, and 2FA code when prompted.
Setup starts the service after login.

## The `corten-matrix` CLI

Run `corten-matrix help` for the available subcommands on either platform:

| Command | Purpose |
|---|---|
| `setup` / `setup-beeper` | Configure a self-hosted / Beeper bridge; rerun to change settings. |
| `start` / `stop` / `restart` / `status` | Control the service, including both configured accounts. |
| `logs` / `logs 1` | Tail the first / second account's log. |
| `sync-status` / `sync-status 1` | Read the account's [backfill status](#inspecting-backfill-status), even with the daemon stopped. |
| `login` | Repeat interactive Apple login. |
| `install-service` / `uninstall-service` | Install or remove the service; `uninstall` is an alias for removal. See [Management](#management). |
| `reset` | Rebuild bridge state; read [Reset and duplicate-room recovery](#reset-and-duplicate-room-recovery) first. |
| `update` / `update check` / `update force` | Official binaries only: install, preview, or reinstall a release. See [Updating](#updating). |
| `bbctl <args>` | Beeper bridge-manager commands. |

### Dual accounts (two Apple IDs)

Run `corten-matrix setup 1` or `setup-beeper 1` to add or reconfigure a second
Apple ID. Each has its own login, config, database, and Matrix appservice:

| Account | Default data directory | Logs |
|---|---|---|
| First | `~/.local/share/corten-matrix/` | `corten-matrix logs` |
| Second | `~/.local/share/corten-matrix-1/` | `corten-matrix logs 1` |

One service supervises both accounts independently: a crash restarts only the
affected account, while service commands act on both.

Both accounts can use CloudKit backfill. At most one can use local chat.db,
because it contains only the Apple ID signed into Messages on that Mac. This
restriction affects history, not live messaging. Linux uses CloudKit only.

## Updating

Official binaries include these commands:

```bash
corten-matrix update          # install latest release and restart
corten-matrix update check    # preview version and release notes
corten-matrix update force    # reinstall current release
```

The updater follows the `corten-matrix` symlink on your `PATH`, replaces its
target, and preserves config, login, and data. Run setup or `install-service`
and accept the PATH symlink first; it will not guess an installation path.
It uses `sudo` if replacement requires it.

Source builds have no `update` command. Pull this repo, rebuild, and run
`corten-matrix restart`.

## Login

Setup logs in when no saved session exists. To repeat it, run
`corten-matrix login`, or DM the bridge bot and choose **Apple ID (External Key)**.
Prompts cover Apple ID, password, 2FA when needed, and outgoing handle selection;
Linux also requires the extracted hardware key. A Mac signed into the same
iCloud account can log in without 2FA.

Rejected verification codes can be retried in the same session. Verification
and network errors can also be retried without consuming a wrong-code attempt.

The outgoing handle is the phone number or email recipients see. Change it by
logging in again or setting `preferred_handle` in [Configuration](#configuration).

### SMS Forwarding

To bridge SMS (green bubble) messages, enable forwarding on your iPhone:

**Settings → Messages → Text Message Forwarding** → toggle on the bridge device.

### Receiving messages

Live iMessages create Matrix rooms automatically. History backfill is optional
and off by default. Enable it during setup; CloudKit is the default source and
requires your device PIN during login to join iCloud Keychain.

`backfill_source: chatdb` is a legacy macOS fallback. It reads
`~/Library/Messages/chat.db` and requires **System Settings → Privacy & Security →
Full Disk Access** for the bridge binary or Terminal. If CloudKit cannot run on
an older Mac, prefer running the bridge on Linux with CloudKit when possible.

On the first install, setup offers an uncapped backfill or a per-chat limit
(minimum 100). Later changes to `backfill.max_initial_messages` require editing
YAML; see [Configuration](#configuration).

## Bridge commands

The bot DM is the **iMessage** management room. Missing default names/avatars are
repaired on reconnect; custom values are preserved.

Type commands without a prefix there (`help`, `start-chat`). In a bridged DM or
group, use `!im help` or `!im start-chat`. Type `cancel` (or `!im cancel`) to abort
an interactive picker.

### Common commands

| Command | What it does |
|---|---|
| `start-chat` | Open a new iMessage DM. With no arguments, the bot walks you through phone vs. email and explains the country-code format. With an argument (`start-chat +15551234567` or `start-chat someone@icloud.com`) it skips the picker. |
| `contacts` | Search your synced contacts by name (iCloud, external CardDAV, or local macOS Contacts depending on `backfill_source` and `carddav` settings) and reply with a number to open a chat. Different from `start-chat` — use this when you don't remember the number/email. Alias: `find`. |
| `restore-chat` | List iMessage chats in the recycle bin. Reply with a number to bring one back, including its history. |
| `sync-status` | Show CloudKit zone state, ingested chats, bridgeable/delivered/pending messages, excluded rows, and backfill queue progress. |
| `logout` | Sign out of iMessage. Lists active handles, you reply with a number (or `all`). The bot then walks you through the manual step at `appleid.apple.com → Devices` to fully revoke the bridge from Apple's servers. |
| `help` | Full command list, grouped by section. |

### Phone-number format for `start-chat`

Include `+` and the country code, for example `start-chat +15551234567`.
Spaces, dashes, and parentheses are stripped. Email addresses also work;
`tel:` / `mailto:` prefixes are optional. A bare local number is not accepted.

### Logging out

`logout` does the bridge-side teardown automatically — disconnects from Apple, removes the login from the bridge, kicks you from portals, and wipes the local session backup so a re-login starts from a clean slate.

The bridge has no API to deregister your IDS identity from Apple, so the success message walks you through the final step:

1. Sign in at <https://appleid.apple.com>.
2. Go to **Devices**.
3. Find the entry for the bridge (often shown as a Mac, sometimes named "Apple Device").
4. Click **Remove from account**.

Until you do step 4, Apple still considers the bridge a registered iMessage device.

## FaceTime

Incoming and missed FaceTime calls post notices in the portal. Answer on an
Apple device; Matrix cannot place or answer calls. Set `disable_facetime: true`
or turn notices off during setup to hide them without changing registration.

## Focus & Do Not Disturb

Contacts on iOS 18+ can appear as **Alice 🌙** while Focus/DND is on. The bridge
updates the DM title and ghost presence in place, without posting a message or
unarchiving the chat. Groups do not show per-member Focus.

`statuskit_notifications: false` hides the indicator and presence updates while
retaining registration. Leave `statuskit_share_on_startup: true` for the startup
key exchange that helps receive contacts' status. Both settings are described
in the [config reference](pkg/imconfig/example-config.yaml).

## Shared Albums

iCloud Shared Albums (Photo Streams) you subscribe to surface as dedicated rooms with the album's photos and videos backfilled. Use:

| Command | What it does |
|---------|-------------|
| `shared-albums` | Browse available Shared Albums; pick one, then pick assets to download. |
| `shared-subscribe <album-id>` | Subscribe to a Shared Album by ID so the bridge watches it for new assets. |
| `shared-subscribe-token <token>` | Subscribe via the one-time invitation token from an iCloud share URL (`icloud.com/sharedalbum/...`). |
| `shared-unsubscribe <album-id>` | Unsubscribe from an album so the bridge stops watching it. |
| `shared-state` | Dump current Shared Streams state as JSON (debugging). |

A full list lives under `!im help` in the **Shared Streams** section.

## Image and video conversion

| Direction | Conversion | Setting |
|---|---|---|
| Both | TIFF → JPEG; Opus voice notes between CAF and OGG without re-encoding | Always on |
| Matrix → iMessage | Non-JPEG images → JPEG, also updating the Matrix event | Always on |
| iMessage → Matrix | HEIC/HEIF → JPEG, preserving EXIF/ICC/XMP and normalizing orientation; multi-image files use the primary frame | `heic_conversion`; quality via `heic_jpeg_quality` |
| iMessage → Matrix | Non-MP4 video → MP4; stream-copy first, H.264/AAC fallback | `video_transcoding` |

Live Photos bridge only the still image, with optional HEIC conversion; their
motion companion is not sent as a video. Ordinary videos are unaffected.

Attachments over `max_attachment_size_mb` (default 100) are skipped before
download. Raise this only if the homeserver accepts larger files and the host
has enough RAM; attachments are buffered in memory.

Setup installs `libheif` regardless of the conversion setting, and `ffmpeg`
when video conversion is enabled.

## How It Works

Matrix client ↔ homeserver ↔ corten-matrix ↔ rustpush ↔ Apple IDS/APNs.
Live traffic arrives through APNs; optional history comes from CloudKit or
macOS chat.db. Contacts come from iCloud CardDAV, configured external CardDAV,
or local macOS Contacts. Shared names/photos use CloudKit independently of
message backfill. Background refreshes retain cached profiles on failure and
back off after rate limits or missing records; incoming announcements can
refresh them immediately.

For chat.db, contact aliases share a stable DM portal, literal Apple GUIDs are
retained for history, and current SMS/iMessage routing is tracked separately.
Existing populated rooms are preserved; older histories cannot revert a newer
route. See [engineering invariants](docs/ENGINEERING.md) for the implementation
contracts. Existing duplicate Matrix histories require investigation before
[recovery](#reset-and-duplicate-room-recovery); they are not merged automatically.

### Inspecting backfill status

Run `sync-status` in the management room, or `corten-matrix sync-status`
(`sync-status 1` for the second account) on the host. It reads persisted
CloudKit zones, eligible/delivered/pending messages, exclusions, and backfill
queue state without migrating or writing to the database.

The host command works with the daemon stopped, but cannot report live sync or
batch-send capability. A 100% delivery figure covers currently cached,
bridgeable CloudKit messages, not future Apple events or all other features.

## Privacy

Bridge message records store routing metadata. CloudKit backfill additionally
stages plaintext in the local `cloud_message` cache; chat.db backfill reads
Apple's database directly without copying bodies into this cache.

Every five minutes, the scrubber clears cached text, subject, sender, and
reaction text after delivery to Matrix and a five-minute ingestion grace
period. Deleted/unsent and permanently excluded content is also scrubbed.
Pending eligible history is retained for delivery. Deletes and unsends clear
cached bodies immediately; scrub errors block completion of the removal so
plaintext remains eligible for cleanup.

SQLite uses secure deletion to zero freed plaintext. PostgreSQL only clears
columns; old bytes can remain in dead tuples until vacuuming. Attachment
metadata, group names, and participant handles remain for routing and retries.
The cache is not the canonical history; Messages in iCloud is.

Connector logs replace handles with stable hash tokens and reduce URLs to
scheme/host. **Framework and Rust diagnostics can still contain raw identifiers,
URLs, or authentication details.** Keep logs local and redact before sharing.

`debug_disable_privacy` is development-only: it disables connector log
anonymization and the scrubber, then re-fetches previously scrubbed CloudKit
plaintext. It does not undo deletes/unsends or redeliver messages. Leave it
`false` in real deployments.

## Management

Use `corten-matrix start`, `stop`, `restart`, `status`, and `logs` on either
platform. `uninstall-service` removes the service; `install-service` refuses to
overwrite units without its managed marker.

Each [account directory](#dual-accounts-two-apple-ids) contains `logs/bridge.log`
(structured JSON) and `logs/bridge.stdout.log` (raw stdout/crash output).
`corten-matrix logs` renders the structured log readably.

### macOS

The LaunchAgent is `com.lrhodin.corten-matrix`. For direct service management:

```bash
launchctl kickstart -k gui/$(id -u)/com.lrhodin.corten-matrix  # restart
launchctl bootout gui/$(id -u)/com.lrhodin.corten-matrix       # stop until next login
```

### Linux

The systemd unit is `corten-matrix`. Setup normally uses user scope; on a fresh
install without a user bus (such as LXC), it uses system scope instead. If an
existing user unit's manager is unreachable, setup stops instead of creating
a second service. The CLI detects installed scope and uses `sudo` when needed.

If units exist in both scopes, setup/start/restart/install stop until you choose
and remove one. Stop attempts both scopes; uninstall removes both and reports
failures. For direct management, use `systemctl --user` / `journalctl --user`
for user scope, or omit `--user` for system scope:

```bash
systemctl --user status corten-matrix
journalctl --user -u corten-matrix -f
```

Without a service, run `./corten-matrix -c <account-directory>/config.yaml`.

## Configuration

Setup generates `config.yaml` in the [account data directory](#dual-accounts-two-apple-ids).
Set `XDG_DATA_HOME` before setup to override the base directory.

### Reconfiguring without editing YAML

Rerun `corten-matrix setup` / `setup-beeper` (add `1` for the second account) to
change the outgoing handle, CardDAV, FaceTime/Focus notices, or media conversion
settings without wiping state. Linux also offers a backfill on/off toggle.
macOS preserves existing backfill settings; its source-selection prompt appears
only with a fresh database. Edit YAML to change backfill settings without a
setup prompt.

Options without a setup prompt require editing YAML and running
`corten-matrix restart`. This includes read receipts, typing indicators,
attachment limits, Unknown Senders, and later changes to the initial backfill
cap. Enabling `bridge_filtered_chats` can create rooms for delivery notices,
2FA codes, and other iCloud **Unknown Senders** messages.

### Reset and duplicate-room recovery

**`corten-matrix reset` deletes the primary Beeper registration, which may remove
all Matrix rooms owned by it. Restoring local files cannot undo that deletion.**
It also removes disposable local config, the default SQLite database, and logs.

The default preserves Apple/iMessage identity, config backups, and unknown files.
Before deletion it verifies shutdown and validates the saved session/keystore,
including CloudKit credentials when needed. A failed final session refresh can
use the previous valid atomic backup. Validation failures stop recovery.

Only the primary Beeper account with the default SQLite layout is supported.
Second accounts, self-hosted installs, custom SQLite paths, PostgreSQL, and
unknown options are rejected before stopping the bridge. Use the command,
not `scripts/reset-bridge.sh` directly.

For duplicate-DM recovery, first investigate the affected rooms rather than
assuming an empty room is damaged. Once a rebuild is warranted:

1. Install the binary containing the identity fix, then back up the data directory.
2. Run `corten-matrix reset` and read the warning before confirming.
3. Run `corten-matrix setup-beeper`; it reuses the preserved Apple session.
4. Verify new traffic and history use one room per DM before archiving old duplicates.

`corten-matrix reset --delete-imessage-state` also discards Apple identity and
forces a fresh login. Interactive use requires the separate exact confirmation
`DELETE IMESSAGE STATE`. This flag is not needed to rebuild the bridge database.

### Key options

The commented [network config](pkg/imconfig/example-config.yaml) is the reference
for connector settings and defaults, including contacts, backfill, media,
privacy, read receipts, and typing. Avoid copying it wholesale over an existing
configuration.

Framework settings also used by setup:

| Field | Default | Purpose |
|---|---|---|
| `backfill.max_initial_messages` | `2147483647` | Initial per-chat cap; this value means uncapped. |
| `encryption.allow` | `false` | Enable end-to-bridge encryption. |
| `database.type` | `sqlite3-fk-wal` | SQLite is primary; PostgreSQL is an unsupported, untested fallback. |

## Build from source (macOS)

Requires macOS 13+, an iCloud sign-in, Xcode Command Line Tools
(`xcode-select --install`), and a checkout path without spaces. Linux users must
use release binaries because the public source build uses Apple's native NAC.

```bash
git clone https://github.com/lrhodin/corten-matrix.git
cd corten-matrix
make
```

`make` installs missing Homebrew dependencies, prepares the pinned rustpush
source and patches, and builds `librustpushgo.a` plus the self-contained
`corten-matrix` binary. Run `./corten-matrix setup` or `setup-beeper` afterward.
Other targets: `make rust`, `make bindings`, and `make clean`.

Contributors should read [AGENTS.md](AGENTS.md) for editing and verification
commands and [engineering invariants](docs/ENGINEERING.md) for lifecycle rules.

### Maintainer release builds

With the private OpenCider deploy key, manually run **Build release binaries**
in GitHub Actions. Choose Mac, either/both Linux architectures, or All platforms.
Platform-only runs upload binaries without tags or releases.

All platforms requires a release title and creates a draft with three binaries:
universal macOS, Linux amd64, and Linux arm64. Choose a patch/minor/major increment
or an exact version (including for the first release). An existing tag is usable
only if it points directly to the dispatched public commit. Review release notes
and the draft before publishing. See [release provenance](docs/ENGINEERING.md#release-workflow-provenance)
for private-source and tagging contracts.

## Source layout

| Path | Purpose |
|---|---|
| `cmd/corten-matrix/`, `pkg/cli/` | Entrypoint, management CLI, and services |
| `pkg/connector/` | Messaging, identity, contacts, backfill, and privacy |
| `pkg/imconfig/`, `pkg/bbctl/` | Embedded config and Beeper management |
| `pkg/rustpushgo/` | Rust FFI wrapper and generated Go bindings |
| `third_party/` | Pinned rustpush source and build patches |
| `rustpush/open-absinthe/`, `nac-validation/` | Native macOS validation |
| `imessage/` | macOS chat.db reader |
| `scripts/`, `tools/` | Embedded setup scripts and hardware-key extractors |

## Troubleshooting

- **Contact Key Verification:** turn it off under your Apple account settings
  before login (iPhone Settings or Mac System Settings → your name → Contact Key
  Verification).
- **chat.db history is empty:** check [Full Disk Access](#receiving-messages).
- **Extractor blocked:** follow the [Gatekeeper instructions](#step-1-extract-hardware-key-one-time-on-a-mac).
- **Database locked:** the bridge serializes its SQLite pool. Check for another
  bridge process or CLI writing the same database.
- **A conversation stays empty:** startup re-arms portals marked done without
  bridged messages. Look for the re-arm log and inspect [sync-status](#inspecting-backfill-status)
  before considering a reset.
- **Service problems:** start with `corten-matrix status` and `corten-matrix logs`;
  see [Management](#management) for log locations and platform commands.

## Chat With Us

**Chat with us on Matrix**: [Join our Room Here](https://matrix.to/#/#corten-matrix:beeper.com)

## License

MPL 2.0 — see [LICENSE](LICENSE).
