# Project Guide

## What this repo is

`mautrix-imessage` v2 is a Matrix-iMessage bridge. The top-level Go app hosts the bridge and bridgev2 integration, while Rust handles the Apple protocol stack through `rustpush`.

The stack is:

`cmd/mautrix-imessage` (Go entrypoint) -> `pkg/connector` (bridge logic) -> `pkg/rustpushgo` (UniFFI FFI wrapper) -> `third_party/rustpush-upstream` (Rust protocol implementation)

## Primary entrypoints

- `cmd/mautrix-imessage/main.go`: bridge startup, CLI subcommands, permission repair, `PostInit` command registration.
- `pkg/connector/`: most bridge behavior lives here.
- `pkg/rustpushgo/src/lib.rs`: Rust objects and exported FFI surface consumed by Go.
- `third_party/rustpush-upstream/`: vendored Rust protocol stack used by the FFI layer.
- `rustpush/open-absinthe/`: legacy external-key/NAC Rust crate still present outside the upstream mirror.
- `imessage/` and `nac-validation/`: macOS-specific native integrations.
- `tools/extract-key/` and `tools/nac-relay/`: separate utilities for Linux/external-key workflows.

## Build and verify

Use these as the default verification commands:

- `make build`: builds the Rust static library, the main bridge binary/app, and `bbctl`.
- `make rust`: rebuilds only `librustpushgo.a`.
- `make bindings`: regenerates Go FFI bindings and then runs `scripts/patch_bindings.py`.
- `make install`: standalone installer flow.
- `make install-beeper`: Beeper installer flow.
- `make reset`: resets bridge state.
- `make extract-key`: builds the hardware-key extractor on macOS only.

Testing expectations:

- Go tests now exist in targeted packages; run focused `go test` commands for packages you touch.
- On macOS, plain `go test` may not find libolm headers installed by Homebrew. Match the Makefile environment when running Go tests:
  `CGO_CFLAGS="-I/opt/homebrew/include" CGO_LDFLAGS="-L/opt/homebrew/lib -L$(pwd)" go test ./pkg/connector`
  Use `/usr/local` instead of `/opt/homebrew` on Intel Homebrew installs.
- Rust tests exist; run targeted cargo tests when changing Rust behavior:
  - `cargo test --manifest-path third_party/rustpush-upstream/Cargo.toml`
  - `cargo test --manifest-path third_party/rustpush-upstream/open-absinthe/Cargo.toml`
  - `cargo test --manifest-path rustpush/open-absinthe/Cargo.toml` if the legacy crate is touched.
- If you change Go bridge logic, at minimum run `make build` on the current platform.
- If you change the FFI boundary or Rust objects exposed to Go, run `make bindings` and then `make build`.

## Hard constraints

### FFI boundary (Go <-> Rust)

Never hand-edit `pkg/rustpushgo/rustpushgo.go` or `pkg/rustpushgo/rustpushgo.h`.

Always regenerate them with:

```bash
make bindings
make build
```

`make bindings` requires `uniffi-bindgen-go` matching UniFFI `0.25.0`:

```bash
cargo install uniffi-bindgen-go --git https://github.com/NordSecurity/uniffi-bindgen-go --tag v0.2.2+v0.25.0
```

If the generated Go changes, check whether `scripts/patch_bindings.py` also needs an update. That script patches UniFFI output for this repo's CGO and Go 1.24+ requirements.

### Windows bindings

The Makefile's `bindings` target hardcodes UNIX static-lib naming (`librustpushgo.a`). MSVC Rust emits `rustpushgo.lib` instead, so `make bindings` on Windows cannot find the artifact. Use the Windows-native helper scripts under `dev/` instead:

```cmd
dev\windows-dev-env.bat && dev\windows-bindings.bat
```

- `dev\windows-dev-env.bat`: one-shot environment bootstrap for vcvarsall, LIB/INCLUDE, PATH, cargo, Strawberry Perl, CMake, Python, protoc, LLVM, LIBCLANG_PATH, a `python3` shim, and first-run `uniffi-bindgen-go` install. It is idempotent and safe to re-run.
- `dev\windows-bindings.bat`: mirrors `make bindings`; it builds the crate if the `.lib` is missing, runs `uniffi-bindgen-go` against `rustpushgo.lib`, then runs `python3 scripts/patch_bindings.py`. It respects `CARGO_TARGET_DIR` if set, so users with cloud-sync folders can redirect cargo output elsewhere to avoid file-lock races on openssl-sys intermediates.

The one-time winget installs needed for the dev-env script are documented at the top of `dev\windows-dev-env.bat`.

### Generated and derived artifacts

- `pkg/rustpushgo/rustpushgo.go` and `pkg/rustpushgo/rustpushgo.h` are generated.
- `pkg/rustpushgo/target/` is build output.
- `data/config.yaml` and `data/registration.yaml` are generated runtime artifacts when using repo-local development state, not source-of-truth defaults. Installed runs usually use `~/.local/share/mautrix-imessage/config.yaml` instead.

### Path and toolchain constraints

- The repo path must not contain spaces. The `Makefile` hard-fails because CGO and linker paths break.
- Top-level Go module targets Go `1.25.x` (`go.mod` uses `go 1.25.0` and `toolchain go1.25.9`).
- `tools/extract-key/` is its own Go module pinned to Go `1.20` so it can still build on older macOS systems.
- Linux dependency bootstrapping is handled by `scripts/bootstrap-linux.sh`.

## Platform notes

### macOS

- `make build` produces `mautrix-imessage-v2.app`.
- Native NAC validation uses `nac-validation/` and Apple frameworks.
- `chat.db` backfill and local contacts require Full Disk Access.
- Useful runtime command after a build for an installed config:

```bash
./mautrix-imessage-v2.app/Contents/MacOS/mautrix-imessage-v2 -c ~/.local/share/mautrix-imessage/config.yaml
```

- If using repo-local development state and `data/config.yaml` exists, run:

```bash
./mautrix-imessage-v2.app/Contents/MacOS/mautrix-imessage-v2 -c data/config.yaml
```

### Linux

- `make build` produces a plain binary.
- The `hardware-key` cargo feature is enabled automatically on Linux.
- Linux NAC uses `third_party/rustpush-upstream/open-absinthe/`, the legacy `rustpush/open-absinthe/`, or `tools/nac-relay/` for Apple Silicon relay flows depending on the code path being changed.

## Important code areas

### `pkg/connector/`

This is the main area for bridge behavior:

- `connector.go`: connector lifecycle, startup behavior, backfill defaults, auto-restore.
- `client.go`: message send/receive, reactions, edits, typing, backfill behavior.
- `login.go`: Apple ID and external-key login flows.
- `chatdb*.go`: macOS chat.db access and backfill.
- `config.go` + `example-config.yaml`: bridge-specific config schema and defaults.
- `ids.go`: portal/identifier conversion. Changes here can require DB resets.
- `cloud_contacts.go`, `external_carddav.go`, `contact_merge.go`: contact resolution.
- `permissions_darwin.go`: Full Disk Access checks and prompts.

Platform-specific Go code generally follows `_darwin.go` and `_other.go` splits.

### `pkg/rustpushgo/`

- `src/lib.rs` defines the Rust wrapper types exposed to Go.
- `build.rs` compiles the macOS hardware-info Objective-C shim.
- Changes here often require coordinated Rust, generated binding, and Go call-site updates.

### `third_party/rustpush-upstream/`

This is a vendored dependency tree, but it is edited in-repo through path dependencies. Keep changes intentional and scoped. If you change Rust types or APIs consumed by Go, audit `pkg/rustpushgo/src/lib.rs` at the same time.

## Keep these in sync

Several behaviors are duplicated across Go code and shell installers. If you change one side, audit the others in the same patch:

- FFI generation rules:
  - `pkg/rustpushgo/src/lib.rs`
  - `scripts/patch_bindings.py`
  - generated `pkg/rustpushgo/rustpushgo.go` / `.h`
  - `dev/windows-dev-env.bat`
  - `dev/windows-bindings.bat`
- Backfill defaults and behavior:
  - `pkg/connector/connector.go`
  - `pkg/connector/config.go`
  - `pkg/connector/example-config.yaml`
  - `scripts/install.sh`
  - `scripts/install-linux.sh`
  - `scripts/install-beeper.sh`
  - `scripts/install-beeper-linux.sh`
- Broken-permissions repair for Beeper installs:
  - `cmd/mautrix-imessage/main.go`
  - `scripts/install-beeper.sh`
  - `scripts/install-beeper-linux.sh`
- CardDAV setup/config expectations:
  - `cmd/mautrix-imessage/carddav_setup.go`
  - `pkg/connector/config.go`
  - installer scripts

## Runtime and stateful behavior

- The bridge may keep runtime state under `data/` during repo-local development, but install flows use `~/.local/share/mautrix-imessage/` by default. Do not assume `data/` exists on a machine that is running an installed LaunchAgent or systemd service.
- macOS installer LaunchAgents write stdout/stderr to `~/.local/share/mautrix-imessage/bridge.stdout.log` and `~/.local/share/mautrix-imessage/bridge.stderr.log`.
- Structured bridge logs may also be under `~/.local/share/mautrix-imessage/logs/bridge.log` with rotated siblings in the same directory. When debugging runtime behavior on this machine, check the top-level stdout/stderr files first, then `logs/bridge.log`.
- Linux systemd installs usually surface logs through `journalctl --user -u mautrix-imessage -f` or `journalctl -u mautrix-imessage -f`.
- `tools/nac-relay/` stores relay credentials and cert material under `~/Library/Application Support/nac-relay/`.
- `cmd/bbctl` stores config under the user's config dir, typically `~/Library/Application Support/bbctl/config.json` on macOS or the Linux equivalent from `os.UserConfigDir()`.
- Beeper install flows now use `~/.local/share/mautrix-imessage/bbctl`. An older full-repo clone at `~/.local/share/mautrix-imessage/bridge-manager` can interfere with `make install-beeper` and may need to be removed.

## Reset-risk changes

Call out reset risk explicitly when a change affects persisted IDs or session state. A full reset may be warranted for changes involving:

- portal ID or key format
- bridge DB schema or metadata layout
- login/session restore state
- backfill source or logic that assumes fresh portal state
- removal of legacy portal types still stored in the DB

Relevant files include `pkg/connector/ids.go`, `pkg/connector/dbmeta.go`, `pkg/connector/identity_store.go`, and migration-sensitive logic in `client.go`.

## Useful subcommands

The main bridge binary supports a few repo-specific subcommands that matter during debugging:

- `login`
- `check-restore`
- `list-handles`
- `carddav-setup`
- `init-db`

## Good task framing for this repo

When assigning work, it helps to specify:

- target platform: macOS or Linux
- login mode: Apple ID or external hardware key
- install mode: standalone or Beeper
- whether CloudKit backfill or chat.db backfill is in scope
- whether a DB reset is acceptable
- how you want the change verified: `make build`, targeted cargo test, installer exercise, or runtime smoke test

## Documentation hygiene

If you change a user-facing workflow or a hard engineering constraint, update the docs in the same patch:

- `README.md` for install and operator-facing behavior
- `AGENTS.md` for repo-specific agent guidance
- `CLAUDE.md` if the longer internal engineering notes also need to stay aligned
