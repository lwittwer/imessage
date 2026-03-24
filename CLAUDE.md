# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Is

mautrix-imessage v2 — a Matrix-iMessage puppeting bridge using rustpush and mautrix bridgev2. Connects directly to Apple's iMessage servers via APNs/IDS without SIP bypass or relay servers. Contact Key Verification must be disabled for the bridge to function.

## Build Commands

```bash
make install-beeper  # Full build + Beeper install (primary workflow)
make build           # Build .app bundle (macOS) or binary (Linux) + bbctl
make rust            # Build Rust static library only (librustpushgo.a)
make bindings        # Regenerate Go FFI bindings (requires uniffi-bindgen-go, also runs scripts/patch_bindings.py)
make clean           # Remove build artifacts
make reset           # Reset bridge state (deletes config/database)
make extract-key     # Build hardware key extraction tool (macOS only)
```

First build takes ~3 minutes for the Rust compilation. No Go test suite; Rust tests exist in `rustpush/` subcrates (`cargo test` from within `rustpush/`).

## Running / Restarting for Development

On macOS after `make build`:
```bash
./mautrix-imessage-v2.app/Contents/MacOS/mautrix-imessage-v2 -c data/config.yaml
```

Restart the running bridge (managed by LaunchAgent):
```bash
launchctl kickstart -k gui/$(id -u)/com.lrhodin.mautrix-imessage
```

Config and database live in `data/`. Bridge logs: `~/.local/share/mautrix-imessage/bridge.stdout.log` (and `bridge.stderr.log`).

Subcommands: `login`, `check-restore`, `list-handles`, `carddav-setup`, `init-db`.

## Architecture

```
Matrix Client <-> Homeserver <-> mautrix-imessage (Go, bridgev2) <-> rustpush (Rust, via UniFFI FFI) <-> Apple IDS/APNs
```

**Three-layer stack:**

1. **Go bridge layer** (`pkg/connector/`) — Implements mautrix bridgev2 connector interface. Key files:
   - `connector.go` — bridge lifecycle, platform detection (`IMConnector` implements `bridgev2.NetworkConnector`)
   - `client.go` — message send/receive, reactions, edits, typing (`IMClient` implements `bridgev2.NetworkAPI`)
   - `login.go` — two login flows: Apple ID (direct) and External Key (hardware key for Linux)
   - `chatdb.go` — chat.db backfill (macOS only, requires Full Disk Access)
   - `config.go` — bridge config schema (embeds `example-config.yaml`)
   - `ids.go` — identifier/portal ID conversion

2. **Rust FFI boundary** (`pkg/rustpushgo/`) — UniFFI 0.25.0 static library bridging Go and Rust.
   - `src/lib.rs` — `Wrapped*` types (e.g., `WrappedAPSState`, `WrappedAPSConnection`) annotated with `#[derive(uniffi::Object)]` that wrap inner rustpush types for the FFI boundary
   - `rustpushgo.go` / `rustpushgo.h` — **auto-generated, never hand-edit** (regenerate with `make bindings`)
   - Install uniffi-bindgen-go: `cargo install uniffi-bindgen-go --git https://github.com/AO-AO/uniffi-bindgen-go --tag v0.2.2+v0.25.0`

3. **Rust protocol layer** (`rustpush/`) — Vendored OpenBubbles/rustpush. Handles IDS registration, APNs push, iMessage encryption/decryption, CloudKit, FaceTime, FindMy.

**Entrypoint:** `cmd/mautrix-imessage/main.go` creates `mxmain.BridgeMain` with the `IMConnector`, registers custom bridge commands via `PostInit`, and handles subcommands before normal bridge startup.

**NAC validation** (Apple device attestation):
- macOS: native `AAAbsintheContext` via Objective-C (`nac-validation/`)
- Linux/Intel key: x86_64 emulation via open-absinthe (`rustpush/open-absinthe/`)
- Linux/Apple Silicon key: relay to a Mac running `tools/nac-relay/`

## Key Directories

| Directory | Purpose |
|-----------|---------|
| `cmd/mautrix-imessage/` | Go entrypoint — main bridge binary |
| `cmd/bbctl/` | Beeper bridge controller CLI |
| `pkg/connector/` | Core bridge logic (~30 Go files) |
| `pkg/rustpushgo/` | Rust FFI crate + generated Go bindings |
| `rustpush/` | Vendored iMessage protocol library (Rust) |
| `nac-validation/` | macOS NAC via AppleAccount.framework |
| `imessage/` | Go iMessage data structures; `mac/` subdir has Objective-C for chat.db/Contacts |
| `tools/` | extract-key (CLI+GUI), nac-relay (CLI+GUI) |
| `scripts/` | Install scripts, bootstrap, binding patches |

## Connector Pattern

`IMConnector` in `pkg/connector/connector.go` implements `bridgev2.NetworkConnector`. Each logged-in user gets an `IMClient` (in `client.go`) implementing `bridgev2.NetworkAPI`. The bridge framework handles Matrix-side concerns; the connector handles iMessage-side concerns.

Platform-specific code uses Go build tags: `_darwin.go` / `_other.go` suffixes (e.g., `chatdb_darwin.go`, `contacts_local_darwin.go`, `permissions_darwin.go`).

Contact resolution uses iCloud CardDAV or an external CardDAV server (configured via `carddav` in config). On macOS with Full Disk Access, local Contacts are also available.

## Platform Differences

- **macOS**: Builds `.app` bundle, uses native NAC, can read chat.db for backfill. Requires macOS 13+.
- **Linux**: Plain binary, uses open-absinthe (x86 emulation) or NAC relay. Cargo feature `hardware-key` is auto-enabled.

## Database Reset

The bridge database lives at `~/.local/share/mautrix-imessage/mautrix-imessage.db` (SQLite). A full reset via `bbctl delete <bridge>` + wiping that directory is sometimes warranted after changes that affect portal ID format, schema, or session-scoped in-memory state (like `smsPortals`).

**Prompt a reset recommendation when changes involve:**
- Portal ID format or key structure (changes to `ids.go`, `makePortalKey`, portal naming logic)
- New in-memory maps that are populated from incoming messages (session-scoped state won't reflect history)
- DB schema migrations that alter existing portal/message records
- Removing legacy portal types that persist in the DB but are no longer generated by code

**Reset procedure (Beeper workflow):**
1. Stop bridge: `launchctl bootout gui/$(id -u)/com.lrhodin.mautrix-imessage`
2. `bbctl delete <bridge>` — removes bridge registration + cleans up Matrix rooms on Beeper
3. `rm -rf ~/.local/share/mautrix-imessage/`
4. `make install-beeper` — rebuilds and reinstalls; then re-login to Apple ID

## Development Notes

- Go module: `github.com/lrhodin/imessage` (Go 1.25.0, toolchain go1.25.7)
- Rust edition 2021, no workspace — each crate has its own Cargo.toml/Cargo.lock
- CGO is required (links against Rust static lib + platform libraries). Project path must not contain spaces.
- No CI/CD pipeline; no linter configuration
- `.editorconfig`: tabs for code, spaces for markdown/YAML
- Logging: `zerolog` (Go), standard `log`/`tracing` (Rust)
