# CLAUDE.md — mautrix-imessage

This file is for AI assistants (Claude Code, Copilot, etc.) working on this codebase. It describes the project structure, build workflow, code conventions, and architecture patterns needed to make correct, idiomatic changes.

## Project Overview

**mautrix-imessage (v2)** is a Matrix-iMessage puppeting bridge. It allows users to send and receive iMessages from any Matrix client by connecting *directly* to Apple's iMessage servers—no SIP bypass, no Barcelona relay, no cloud relay required.

Key facts:
- Go module: `github.com/lrhodin/imessage`
- License: AGPL-3.0
- Minimum macOS: 13.0 (Ventura)
- Go version: 1.25.0 (toolchain 1.25.7)
- Rust: vendored via `rustpush/` + FFI wrapper in `pkg/rustpushgo/`
- Matrix framework: [`maunium.net/go/mautrix/bridgev2`](https://mau.fi/blog/megabridge-twilio/)

## Repository Layout

```
cmd/
  mautrix-imessage/        # Bridge entry point
    main.go                #   App lifecycle, permission repair, subcommands
    login_cli.go           #   Interactive login (TTY)
    setup_darwin.go        #   macOS: FDA + Contacts permission dialogs
    setup_other.go         #   Linux: no-op stubs
    carddav_setup.go       #   CardDAV URL discovery + password encryption
  bbctl/                   # Beeper bridge-manager CLI
    main.go, auth.go, register.go, delete.go

pkg/
  connector/               # bridgev2 connector (core bridge logic)
    connector.go           #   IMConnector: Init/Start, login flows, auto-restore
    client.go              #   IMClient: send/recv/reactions/edits/typing
    login.go               #   AppleIDLogin + ExternalKeyLogin login flows
    chatdb.go              #   chat.db backfill + contacts reader (macOS)
    chatdb_darwin.go       #   macOS-specific CGO chat.db binding
    contacts_local_darwin.go  # macOS Contacts.app integration
    contacts_local_other.go   # No-op stub for Linux
    cloud_contacts.go      #   iCloud CardDAV contact resolution
    external_carddav.go    #   External CardDAV server (Google, Nextcloud, etc.)
    cloud_backfill_store.go   # CloudKit sync-state DB + message storage
    sync_controller.go     #   CloudKit backfill orchestration
    contact_merge.go       #   Multi-handle contact dedup
    carddav_crypto.go      #   AES-256-GCM password encryption for CardDAV
    command_contacts.go    #   Bridge bot contact commands
    commands.go            #   restore-chat, other bot commands
    capabilities.go        #   Supported message types & features
    ids.go                 #   networkid.UserID / PortalID helpers
    identity_store.go      #   IDS / identity management
    config.go              #   IMConfig struct + example-config.yaml embed
    dbmeta.go              #   UserLoginMetadata struct
    bridgeadapter.go       #   bridgev2 interface glue
    audioconvert.go        #   Audio format transcoding
    urlpreview.go          #   URL preview attachment handling
    util.go                #   Shared helpers
    permissions_darwin.go  #   macOS permission status checks
    permissions_other.go   #   Linux stubs
    example-config.yaml    #   Embedded default config (via //go:embed)
  rustpushgo/              # Go ↔ Rust FFI wrapper (uniffi-generated)
    rustpushgo.go          #   Generated Go bindings
    rustpushgo.h / .c      #   Generated C bridge
    src/lib.rs             #   Rust uniffi entry point

rustpush/                  # Vendored OpenBubbles/rustpush (Rust)
  src/                     #   iMessage protocol, APNs, IDS, CloudKit
  apple-private-apis/      #   iCloud auth, ANISETTE, SRPL
  open-absinthe/           #   NAC x86_64 emulator (unicorn-engine)
    src/bin/enrich_hw_key  #   Enrich Intel keys missing _enc fields
  keystore/                #   Encrypted credential storage
  cloudkit-proto/          #   Protobuf definitions for CloudKit

nac-validation/            # macOS NAC via AppleAccount.framework (Objective-C)
  src/validation_data.h
  src/validation_data.m

imessage/                  # macOS chat.db + Contacts (CGO + Objective-C)
  mac/
    database.go            #   SQLite chat.db queries
    messages.go            #   Message parsing
    contacts.go, groups.go #   Contact / group helpers
    send.go                #   Send helpers (macOS only)
    meow*.h / *.m          #   Objective-C wrappers

ipc/                       # IPC helpers (ipc.go)

tools/
  extract-key/             # Hardware key extraction (Go CLI, run on Mac)
  extract-key-app/         # Hardware key extraction GUI (SwiftUI, x86_64)
  nac-relay/               # NAC relay HTTP server (Go, run on Apple Silicon Mac)
  nac-relay-app/           # NAC relay menubar app (SwiftUI, arm64)

scripts/
  bootstrap-linux.sh       # Install Go/Rust/deps on Ubuntu
  install.sh               # Self-hosted install (macOS)
  install-linux.sh         # Self-hosted install (Linux)
  install-beeper.sh        # Beeper install (macOS)
  install-beeper-linux.sh  # Beeper install (Linux)
  reset-bridge.sh          # Wipe and restart bridge
  patch_bindings.py        # Post-process uniffi-generated bindings

docs/                      # Deep-dive technical notes
  cloudkit-guide.md        # CloudKit sync architecture
  apple-auth-research.md   # Apple auth token details
  group-id-research.md     # Chat/portal ID format mapping
```

## Build Commands

```bash
make build          # Build bridge binary/app + bbctl (runs check-deps automatically)
make rust           # Build Rust static library only (librustpushgo.a)
make bindings       # Regenerate Go FFI bindings (requires uniffi-bindgen-go installed)
make clean          # Remove build artifacts (binary, .app, .a, cargo target)
make install        # build + install as LaunchAgent/systemd service (self-hosted)
make install-beeper # build + install with Beeper credentials
make extract-key    # Build hardware key extraction CLI (macOS only)
```

**Important:** The project path must not contain spaces — CGO/linker will fail.

**macOS output:** `mautrix-imessage-v2.app/Contents/MacOS/mautrix-imessage-v2`
**Linux output:** `mautrix-imessage-v2`

### Rust library

Built from `pkg/rustpushgo/` and copied to the repo root as `librustpushgo.a`. CGO links against it. Rebuild with `make rust` whenever Rust sources change. Rebuilding takes several minutes on first run.

Features:
- **macOS:** native `AAAbsintheContext` (no emulation needed)
- **Linux:** `--features hardware-key` adds the open-absinthe unicorn-engine NAC emulator

### Regenerating FFI bindings

After changing the Rust `#[uniffi::export]` surface in `pkg/rustpushgo/src/lib.rs`:

```bash
make bindings
# This runs: uniffi-bindgen-go target/release/librustpushgo.a --library --out-dir ..
# Then:       python3 scripts/patch_bindings.py
```

## Runtime Subcommands

The bridge binary supports several subcommands used by install scripts:

```bash
./mautrix-imessage-v2 -c data/config.yaml          # Normal operation
./mautrix-imessage-v2 login                         # Interactive Apple ID login (TTY)
./mautrix-imessage-v2 check-restore                 # Validate backup session state (exits 0/1)
./mautrix-imessage-v2 list-handles                  # Print available iMessage identities
./mautrix-imessage-v2 carddav-setup                 # Configure CardDAV contact sync
./mautrix-imessage-v2 --setup -c data/config.yaml   # macOS permission dialogs
```

## Code Conventions

### Formatting & style

- **Tabs** for Go source, 4-space tab width
- **Spaces** for YAML/JSON, 2 or 4 spaces
- **Spaces** (2) for Markdown
- Trailing whitespace trimmed; final newline required
- Standard `gofmt` formatting for Go (run before committing)

### License headers

Every Go source file must begin with the AGPL-3.0 copyright block:

```go
// mautrix-imessage - A Matrix-iMessage puppeting bridge.
// Copyright (C) 2024 Ludvig Rhodin
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
```

Use `Tulir Asokan` as co-author where the file originated from mautrix-go upstream.

### Logging

Use **zerolog** throughout. The bridge logger is available via `c.Bridge.Log`:

```go
log := c.Bridge.Log.With().Str("component", "imessage").Logger()
log.Info().Str("key", value).Msg("doing thing")
log.Warn().Err(err).Msg("something went wrong")
log.Debug().Any("data", data).Msg("verbose detail")
```

Never use `fmt.Println` or `log.Printf` in bridge code. Use `fmt.Fprintf(os.Stderr, ...)` only for pre-bridge startup messages (e.g., in `main.go`).

### Error handling

Wrap errors with context:

```go
if err != nil {
    return fmt.Errorf("failed to do X: %w", err)
}
```

### Platform-specific files

Use build-tag file naming conventions:
- `foo_darwin.go` — compiled only on macOS
- `foo_other.go` — compiled on everything except macOS (add `//go:build !darwin` at top)

### Config changes

Adding a new config field:
1. Add to `IMConfig` in `pkg/connector/config.go`
2. Add `helper.Copy(up.Xxx, "field_name")` in `upgradeConfig()`
3. Add the field (with comment) to `pkg/connector/example-config.yaml`
4. Access via `c.Config.FieldName` or `client.Main.Config.FieldName`

## Architecture

### Connector / Client split

- **`IMConnector`** (`connector.go`) — singleton, manages bridge lifecycle, login flows, config
- **`IMClient`** (`client.go`) — one per logged-in user, implements `bridgev2.NetworkAPI`

The `bridgev2` framework calls:
- `IMConnector.Init/Start` — once at startup
- `IMConnector.LoadUserLogin` — once per user login (creates an `IMClient`)
- `IMClient.*` — per-message/event operations

### Login flows

Two flows, both in `login.go`:
1. **`apple-id`** (`AppleIDLogin`) — macOS only; uses local `IOKit + AAAbsintheContext`
2. **`external-key`** (`ExternalKeyLogin`) — cross-platform; takes a base64 hardware key

Login state is stored in `UserLoginMetadata` (see `dbmeta.go`) and backed up to `session.json` for auto-restore after DB wipe.

### FFI boundary

Go calls Rust through the uniffi-generated bindings in `pkg/rustpushgo/`. All Rust types exposed to Go are prefixed `Wrapped*` (e.g., `WrappedOsConfig`, `WrappedApsConnection`). Never modify `rustpushgo.go` directly — it is generated. Modify `pkg/rustpushgo/src/lib.rs` and re-run `make bindings`.

### Backfill

Two backfill sources, controlled by `BackfillSource` in config:
- **CloudKit** (default) — `sync_controller.go` + `cloud_backfill_store.go`; syncs from iCloud on login
- **chat.db** (macOS only) — `chatdb.go`; reads local SQLite database (requires Full Disk Access)

Backfill is disabled by default. Enable with `cloudkit_backfill: true` in config.

### Contact resolution

Priority order:
1. External CardDAV server (`external_carddav.go`) — if configured
2. iCloud CardDAV (`cloud_contacts.go`) — uses Apple credentials from login
3. Local macOS Contacts.app (`contacts_local_darwin.go`) — macOS only, requires Contacts permission
4. Raw phone/email handle as fallback

### Portal / ID format

See `docs/group-id-research.md` for full details. IDs in `ids.go`:
- User IDs: `tel:+15551234567` or `mailto:user@example.com`
- Group IDs: `iMessage;+;chat<guid>` (DM) or `iMessage;-;<group-guid>` (group)
- Message IDs: raw UUID string from APNs

## Platform Differences

| Aspect | macOS | Linux |
|--------|-------|-------|
| NAC validation | Native `AAAbsintheContext` | x86_64 emulation (Intel key) or NAC relay (Apple Silicon key) |
| chat.db access | Full Disk Access required | Not available |
| Contacts | Contacts.app or CardDAV | CardDAV only |
| Service manager | LaunchAgent (`launchctl`) | systemd user service |
| Binary output | `.app` bundle (code-signed) | Single binary |
| CGO | ObjC frameworks linked | No ObjC |

### Apple Silicon on Linux

Apple Silicon Macs cannot use the x86_64 NAC emulator. Instead:
1. Run `nac-relay` (or `nac-relay-app`) on the Mac — it serves `/validation-data` over HTTPS
2. Extract hardware key with `go run tools/extract-key/main.go -relay https://<mac-ip>:5001/validation-data`
3. The hardware key embeds the relay URL + TLS fingerprint + auth token

## Key Dependencies

| Package | Purpose |
|---------|---------|
| `maunium.net/go/mautrix` | Matrix SDK + bridgev2 framework |
| `maunium.net/go/mautrix/bridgev2` | Modular bridge architecture |
| `github.com/beeper/bridge-manager` | Beeper API (bbctl) |
| `github.com/rs/zerolog` | Structured logging |
| `github.com/urfave/cli/v2` | CLI framework (bbctl) |
| `github.com/mattn/go-sqlite3` | SQLite driver (CGO) |
| `golang.org/x/image` | Image decoding (TIFF, WebP) |
| `gopkg.in/yaml.v3` | YAML config parsing |
| `go.mau.fi/util` | mau utilities (configupgrade, ptr, etc.) |

## Testing

There is currently **no automated Go test suite**. Testing is done by:
1. Building and running the bridge locally
2. Sending/receiving test messages
3. Checking logs at `data/bridge.stdout.log`

The vendored `rustpush/` library has its own Rust test binary (`rustpush-test`) for protocol-level tests.

When making changes, manually verify:
- `make build` succeeds with no errors
- Bridge starts without panics: `./mautrix-imessage-v2 -c data/config.yaml`
- Relevant message flows work end-to-end

## Common Tasks

### Add a new bridge bot command

1. Implement handler in `pkg/connector/commands.go` (or a new file)
2. Register in `cmd/mautrix-imessage/main.go` → `proc.AddHandler(h)` via `connector.BridgeCommands()`

### Add a new message capability

1. Update `pkg/connector/capabilities.go` — add to `GetCapabilities()` return value
2. Handle in `pkg/connector/client.go` — `HandleMatrixMessage()` or `SendMessage()`

### Add a new config option

1. Extend `IMConfig` in `pkg/connector/config.go`
2. Add `helper.Copy` call in `upgradeConfig()`
3. Add documented field to `pkg/connector/example-config.yaml`

### Modify Rust FFI surface

1. Edit `pkg/rustpushgo/src/lib.rs`
2. Run `make rust && make bindings`
3. Update call sites in `pkg/connector/` as needed

### Manage logs

```bash
# macOS
tail -f data/bridge.stdout.log

# Linux (systemd)
journalctl --user -u mautrix-imessage -f
```
