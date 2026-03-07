# CLAUDE.md — mautrix-imessage (v2)

AI assistant guide for this codebase. Read this before making changes.

## Project Overview

**mautrix-imessage v2** is a Matrix-iMessage puppeting bridge that allows sending/receiving iMessages from any Matrix client. It connects directly to Apple's iMessage servers via **rustpush** (no relay or SIP bypass required).

- **License**: AGPL-3.0
- **Go module**: `github.com/lrhodin/imessage`
- **Go version**: 1.25.0 (toolchain 1.25.7)
- **Rust edition**: 2021
- **Platforms**: macOS 13+ (full features), Linux (via extracted hardware keys)

---

## Repository Structure

```
imessage/
├── cmd/
│   ├── mautrix-imessage/     # Main bridge binary entrypoint
│   └── bbctl/                # Bridge control CLI tool
├── pkg/
│   ├── connector/            # bridgev2 connector (core bridge logic, 27 Go files)
│   └── rustpushgo/           # Rust FFI wrapper (uniffi bindings + CGO shim)
├── ipc/                      # IPC framework (JSON message passing between components)
├── rustpush/                 # Vendored rustpush library (Apple protocol impl in Rust)
├── nac-validation/           # macOS NAC validation (Objective-C/Rust)
├── imessage/                 # macOS chat.db + Contacts reader (Go + Objective-C)
│   └── mac/                  # macOS-specific implementations
├── tools/
│   ├── extract-key/          # Hardware key extraction CLI (Go, macOS-only)
│   ├── extract-key-app/      # Hardware key extraction GUI (SwiftUI)
│   ├── nac-relay/            # NAC validation relay server (Go, macOS-only)
│   └── nac-relay-app/        # NAC relay menubar app (SwiftUI)
├── scripts/                  # Install and utility shell scripts
├── docs/                     # Technical architecture documentation
├── prompts/                  # Development research prompts
├── Makefile                  # Build orchestration
├── go.mod / go.sum           # Go dependencies
└── Info.plist                # macOS app bundle metadata
```

---

## Technology Stack

| Layer | Technology |
|---|---|
| Bridge framework | Go + `maunium.net/go/mautrix/bridgev2` |
| Apple protocol | Rust (`rustpush` crate, vendored) |
| Go↔Rust FFI | uniffi v0.25 + CGO |
| macOS integration | Objective-C (IOKit, Contacts, chat.db) |
| Helper tools | Swift/SwiftUI |
| Config format | YAML |
| Database | SQLite (mattn/go-sqlite3) or PostgreSQL |
| Logging | zerolog (`github.com/rs/zerolog`) |
| CLI | `github.com/urfave/cli/v2` |
| IPC | JSON message passing (`ipc/ipc.go`) |

### Key Go Dependencies

| Package | Version | Purpose |
|---|---|---|
| `maunium.net/go/mautrix` | v0.26.3 | Bridge framework (bridgev2) |
| `github.com/mattn/go-sqlite3` | v1.14.34 | SQLite driver (CGO) |
| `github.com/rs/zerolog` | v1.34.0 | Structured logging |
| `github.com/urfave/cli/v2` | v2.27.7 | CLI framework |
| `gopkg.in/yaml.v3` | v3.0.1 | YAML config parsing |
| `golang.org/x/image` | v0.36.0 | Image processing |
| `github.com/gabriel-vasile/mimetype` | v1.4.7 | MIME type detection |
| `go.mau.fi/util` | v0.9.6 | Shared mautrix utilities |
| `github.com/beeper/bridge-manager` | v0.14.0 | Beeper deployment support |
| `github.com/fsnotify/fsnotify` | v1.8.0 | File system event watching |

---

## Build System

### Key Makefile Targets

```bash
make build           # Build binary (+ .app bundle on macOS) and bbctl
make rust            # Build Rust static library only
make bindings        # Regenerate Go FFI bindings (requires uniffi-bindgen-go)
make check-deps      # Auto-install missing dependencies
make install         # Build + install (self-hosted)
make install-beeper  # Build + install for Beeper
make extract-key     # Build hardware key extraction tool (macOS only)
make reset           # Reset bridge state
make clean           # Remove build artifacts
make uninstall       # Remove LaunchAgent (macOS) or display instructions (Linux)
```

### Build Requirements

**CRITICAL**: The project path **must not contain spaces** — CGO and linker flags break with spaces in `SRCDIR`.

**macOS dependencies** (auto-installed via Homebrew):
- `go`, `rust` (cargo), `protoc`, `tmux`, `libolm`

**Linux dependencies** (installed via `scripts/bootstrap-linux.sh`):
- Go, Rust, protobuf, cmake, libssl-dev

### Platform Differences

| Aspect | macOS | Linux |
|---|---|---|
| Output | `mautrix-imessage-v2.app` bundle | `mautrix-imessage-v2` binary |
| NAC validation | Native `AAAbsintheContext` (Objective-C) | open-absinthe x86 emulator |
| Cargo features | _(none)_ | `--features hardware-key` |
| Hardware keys | Read directly from IOKit | Extracted from a Mac via tools |
| Chat history backfill | CloudKit or chat.db | CloudKit only |
| Contacts | Contacts.app or CardDAV | CardDAV only |

### How the Build Works

1. `make rust` compiles the Rust crate at `pkg/rustpushgo/` into `librustpushgo.a`
2. The Go build links the static library via CGO (`CGO_LDFLAGS=-L$(CURDIR)`)
3. On macOS, the binary is placed inside an `.app` bundle and codesigned (`codesign --force --deep --sign -`)
4. `bbctl` is always built as a standalone binary (no CGO)
5. Build-time ldflags inject version, commit hash, and build timestamp

### Regenerating FFI Bindings

Only needed when Rust FFI interface (`pkg/rustpushgo/src/lib.rs`) changes:

```bash
# Requires uniffi-bindgen-go to be installed
make bindings
# This runs uniffi-bindgen-go then scripts/patch_bindings.py
```

---

## Key Source Files

### Go — Bridge Logic (`pkg/connector/`)

| File | Role |
|---|---|
| `connector.go` | `IMConnector` type; bridge lifecycle; platform detection; CloudKit backfill defaults |
| `client.go` | Send/receive messages, reactions, edits, typing indicators |
| `login.go` | Apple ID login flows (password + 2FA); external hardware key login |
| `config.go` | `IMConfig` struct; `IMConfig.FormatDisplayname()`; config upgrade logic |
| `chatdb.go` | macOS chat.db SQLite backfill + contact resolution |
| `chatdb_darwin.go` | macOS-specific chat.db handling |
| `sync_controller.go` | CloudKit backfill orchestration; zone change token management |
| `cloud_backfill_store.go` | SQLite storage for CloudKit synced chats/messages/tokens |
| `ids.go` | iMessage handle ↔ Matrix networkid conversion |
| `capabilities.go` | Bridge feature support declarations |
| `commands.go` | Bridge slash commands |
| `command_contacts.go` | `/contact` slash command; contact search and DM creation |
| `cloud_contacts.go` | iCloud contact name resolution |
| `carddav_crypto.go` | AES-256-GCM encryption/decryption for CardDAV passwords |
| `external_carddav.go` | External CardDAV client (Google, Nextcloud, Fastmail, etc.) |
| `contact_merge.go` | Merging contact data from multiple sources |
| `contacts_local_darwin.go` | macOS Contacts.app integration (loads local contacts) |
| `contacts_local_other.go` | Stub for non-macOS platforms |
| `audioconvert.go` | Audio format conversion for attachments |
| `urlpreview.go` | URL link preview handling |
| `bridgeadapter.go` | `bridgeAdapter` type; satisfies legacy `imessage.Bridge` interface for mac connector compatibility |
| `identity_store.go` | `PersistedSessionState` type; serializes IDS/APS/IDS-identity to `session.json`; prevents "new device" notifications on re-login |
| `dbmeta.go` | Database metadata types: `PortalMetadata`, `GhostMetadata`, `MessageMetadata`, `UserLoginMetadata` |
| `permissions_darwin.go` | macOS permission checking and repair (Full Disk Access, Contacts, etc.) |
| `permissions_other.go` | Stub for Linux |
| `util.go` | `normalizePhone()`, `phoneSuffixes()` and other shared helpers |
| `example-config.yaml` | Embedded default config (via `//go:embed`) |

### Go — Main Entrypoint (`cmd/mautrix-imessage/`)

| File | Role |
|---|---|
| `main.go` | Subcommands: `login`, `check-restore`, `list-handles`, `carddav-setup`; permission repair |
| `login_cli.go` | Interactive login flow (terminal prompts) |
| `setup_darwin.go` | macOS-specific permission setup |
| `setup_other.go` | Linux permission setup |
| `carddav_setup.go` | CardDAV credential configuration |

### Go — Bridge Control CLI (`cmd/bbctl/`)

| File | Role |
|---|---|
| `main.go` | Entry point; subcommands |
| `auth.go` | Authentication commands |
| `register.go` | Bridge registration commands |
| `delete.go` | Bridge deletion commands |

### Go — IPC Framework (`ipc/`)

| File | Role |
|---|---|
| `ipc.go` | JSON bidirectional message passing (`Message`, `OutgoingMessage`, `Processor`); standard error codes (`ErrIPCTimeout`, `ErrUnknownCommand`, etc.) |

The IPC framework is used by `bridgeadapter.go` to interface between the new bridgev2 connector and the legacy macOS `imessage.Bridge` interface.

### Rust (`pkg/rustpushgo/src/lib.rs`)

FFI entry points exposed to Go via uniffi:
- `cloud_sync_chats` — CloudKit chat list sync
- `cloud_sync_messages` — CloudKit message backfill

### Rust (`rustpush/`)

Vendored Apple protocol implementation:
- `src/lib.rs` — APNs connection, IDS protocol
- `src/imessage/cloud_messages.rs` — CloudKit sync, PCS decryption
- `src/icloud/cloudkit.rs` — Low-level CloudKit protobuf API
- `open-absinthe/` — x86 NAC emulator (unicorn-engine, Linux only)

---

## Configuration

### Runtime Config (`data/config.yaml`)

The config file is generated on first run from the embedded `example-config.yaml`. Key fields:

```yaml
# Bridge-specific section
imessage:
  displayname_template: "{{if .FirstName}}{{.FirstName}}{{if .LastName}} {{.LastName}}{{end}}{{else if .Nickname}}{{.Nickname}}{{else if .Phone}}{{.Phone}}{{else if .Email}}{{.Email}}{{else}}{{.ID}}{{end}}"
  cloudkit_backfill: false          # Master on/off for message history sync
  backfill_source: "cloudkit"       # "cloudkit" (default) or "chatdb" (macOS only)
  preferred_handle: ""              # "tel:+15551234567" or "mailto:user@example.com"
  carddav:
    email: ""
    url: ""                         # Leave empty for auto-discovery
    username: ""
    password_encrypted: ""          # AES-256-GCM encrypted, set by carddav-setup
```

**`IMConfig` helper methods:**
- `UseChatDBBackfill()` — true when `cloudkit_backfill=true` AND `backfill_source=chatdb`
- `UseCloudKitBackfill()` — true when `cloudkit_backfill=true` AND `backfill_source` is not `chatdb`
- `FormatDisplayname(DisplaynameParams)` — renders the Go template

### Data Directory

Default: `$HOME/.local/share/mautrix-imessage/`

```
data/
├── config.yaml         # User configuration
├── session.json        # rustpush + IDS session state (auto-restored on restart)
├── keystore/           # Encrypted hardware key material
├── bridge.stdout.log   # Bridge stdout log
├── bridge.stderr.log   # Bridge stderr log
└── *.db                # SQLite database files
```

The `DATA_DIR` variable can be overridden at build time.

### Database Metadata Types (`pkg/connector/dbmeta.go`)

These types are stored as JSON in bridgev2 database metadata columns:

| Type | Key Fields |
|---|---|
| `PortalMetadata` | `ThreadID`, `SenderGuid` (persistent group UUID), `GroupName` (cv_name for outbound routing) |
| `GhostMetadata` | _(empty)_ |
| `MessageMetadata` | `HasAttachments` |
| `UserLoginMetadata` | `Platform`, `ChatsSynced`, `APSState`, `IDSUsers`, `IDSIdentity`, `DeviceID`, `HardwareKey`, `PreferredHandle`, iCloud account fields (`AccountUsername`, `AccountPET`, etc.), `MmeDelegateJSON` |

---

## Code Conventions

### Formatting

Defined in `.editorconfig`:

| File type | Indent style | Indent size |
|---|---|---|
| Go, Rust, Objective-C | tabs | 4 |
| Markdown | spaces | 2 |
| YAML | spaces | (default) |
| All files | LF line endings, UTF-8, final newline |

### Go Style

- Follow standard Go conventions (`gofmt`-compatible)
- Exported symbols: `CamelCase`
- Unexported: `camelCase`
- Error handling: return errors up the stack; log at the call site that handles them
- Use `zerolog` for all logging (structured fields, not `fmt.Printf`)
- Copyright header required on all new Go files (AGPL-3.0, copyright Ludvig Rhodin)

### Rust Style

- Standard Rust conventions (`rustfmt`-compatible)
- `snake_case` for functions/variables, `CamelCase` for types
- `async`/`await` with tokio throughout
- Prefer `?` operator for error propagation

### Platform-Specific Code

Use `_darwin.go` / `_other.go` file suffixes to separate macOS-only code:
- `permissions_darwin.go` / `permissions_other.go`
- `contacts_local_darwin.go` / `contacts_local_other.go`
- `chatdb_darwin.go`
- `setup_darwin.go` / `setup_other.go`

Do NOT use `//go:build` tags as a substitute when the `_darwin.go` pattern works.

### Objective-C (macOS)

- Files in `imessage/mac/` follow ObjC conventions
- Helper utilities prefixed `meow` (e.g., `meowutils.m`)
- CGO bridge calls are thin wrappers — keep Objective-C logic minimal

---

## Testing

There is no formal Go test suite. Rust tests exist for specific subsystems:

```bash
# NAC emulator tests (Linux x86 emulation)
cargo test -p open-absinthe

# Hardware info tests (macOS only)
cargo test -p rustpushgo --features hardware-key

# Full rustpush tests (macOS, requires validation data)
cargo test -p rustpush --features macos-validation-data
```

When adding new features, manually test the login flow and message send/receive on the target platform.

---

## Important Architecture Notes

### Backfill

CloudKit backfill overrides mautrix defaults in `connector.go:Start()` (not `Init()`) because config is loaded after `Init()`:
- `MaxInitialMessages` is set to `math.MaxInt32` (uncapped) when CloudKit backfill is enabled
- `BatchSize` is set to `10000` to prevent fragmented DAG branches in Matrix rooms

### Identity / Handles

iMessage handles are either `tel:+E164` phone numbers or `mailto:` email addresses. Handle ↔ Matrix networkid conversion is in `pkg/connector/ids.go`. The `preferred_handle` config overrides the default outgoing identity.

### Session Persistence (`identity_store.go`)

`PersistedSessionState` serializes the IDS identity, APS push state, and IDS user registration to `session.json`. This prevents Apple from treating re-login as a "new device" (which would trigger "X added a new Mac" notifications to contacts). On startup the bridge reads this file automatically. Do not delete `session.json` unless intentionally resetting the bridge.

### FFI Boundary

The Go↔Rust FFI is generated by uniffi. **Do not manually edit** `pkg/rustpushgo/rustpushgo.go` or `pkg/rustpushgo/rustpushgo.h` — always regenerate with `make bindings` after changing `pkg/rustpushgo/src/lib.rs`.

After regeneration, `scripts/patch_bindings.py` post-processes the generated Go file to fix import paths and compatibility issues.

### Bridge Adapter (`bridgeadapter.go`)

`bridgeAdapter` satisfies the legacy `imessage.Bridge` interface so the existing macOS connector code (`imessage/mac/`) can be reused without modification within the new bridgev2 architecture. It holds a `maulogger` logger, a `zerolog` logger, an `ipc.Processor`, and the platform config.

### CardDAV Password Encryption

CardDAV passwords are stored encrypted (AES-256-GCM) in `config.yaml` under `carddav.password_encrypted`. Use the `carddav-setup` subcommand to set them — never store plaintext passwords in config.

### IPC Framework (`ipc/`)

The `ipc` package provides a bidirectional JSON message-passing system used to bridge the connector with legacy components. Key types:
- `Message` / `OutgoingMessage` — command + id + JSON data payload
- `Processor` — dispatches inbound commands, tracks in-flight requests
- Standard error values: `ErrIPCTimeout`, `ErrUnknownCommand`, `ErrSizeLimitExceeded`, `ErrTimeoutError`, `ErrUnsupportedError`, `ErrNotFound`

---

## Development Workflows

### Adding a New Bridge Feature

1. Define capability in `pkg/connector/capabilities.go`
2. Implement in `pkg/connector/client.go` (receive) or the appropriate handler
3. If the feature requires Rust changes, update `pkg/rustpushgo/src/lib.rs` and run `make bindings`
4. Test on both macOS and Linux if applicable

### Changing Configuration

1. Update `IMConfig` struct in `pkg/connector/config.go`
2. Add upgrade logic to `upgradeConfig()` in the same file
3. Update `pkg/connector/example-config.yaml`
4. Document the new field with a Go doc comment explaining valid values

### Adding a New Subcommand

1. Define the command in `cmd/mautrix-imessage/main.go` using `cli.Command`
2. Implement the handler in a new file under `cmd/mautrix-imessage/`
3. For macOS-only subcommands, use `_darwin.go` / `_other.go` file suffixes

### Platform-Specific Changes

- Always provide a stub in `_other.go` for any macOS-only functionality
- Check `runtime.GOOS == "darwin"` or use `isRunningOnMacOS()` for runtime guards
- `imessage/mac/` Objective-C files are only compiled on macOS (CGO build constraints)

### Adding New Database Metadata Fields

When adding fields to metadata structs in `dbmeta.go`:
- Use `omitempty` JSON tags — bridgev2 stores these as JSON blobs
- `UserLoginMetadata` holds persisted rustpush session data; changes here affect session restore

---

## Git Workflow

- **Default branch**: `master`
- Development branches follow the pattern `claude/<description>-<id>`
- Commit messages should be descriptive and reference the component changed
- No CI pipeline is configured; test manually before pushing

---

## Useful Cross-References

- **CloudKit integration**: `docs/cloudkit-guide.md`
- **Apple authentication / token lifecycle**: `docs/apple-auth-research.md`
- **Group chat IDs and stability**: `docs/group-id-research.md`
- **User install guide**: `README.md`
- **Bridge commands**: `pkg/connector/commands.go`, `pkg/connector/command_contacts.go`
- **Example config**: `pkg/connector/example-config.yaml`
- **IPC message types**: `ipc/ipc.go`
- **Database metadata types**: `pkg/connector/dbmeta.go`
