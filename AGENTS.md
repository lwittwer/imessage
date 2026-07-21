# Project Guide

## Personal Fork Runtime Debugging

On non-main personal branches in this fork, local runtime investigation may
inspect raw logs, config, database rows, Apple handles, URLs, and other
machine-local values when needed to debug the bridge on the owner's machine.
This does **not** relax commit hygiene: do not commit real PII, secrets, raw
runtime artifacts, copied logs, or examples derived from a real account. Keep
raw runtime evidence local and uncommitted; redact or replace it with synthetic
placeholders before writing code comments, tests, docs, commit messages, or
handoff notes. Do not weaken source-level privacy behavior, shipped defaults,
log sanitizers, or scrubbers solely because this local-debugging allowance
exists; make those code/config changes only when the user explicitly asks for
that runtime behavior.

## What This Branch Is

Corten-Matrix is a Matrix-iMessage bridge built on bridgev2 and rustpush. This
beta branch is the single-binary distribution line: the `corten-matrix` binary
is both the bridge and its management CLI (`setup`, `setup-beeper`, `start`,
`stop`, `restart`, `status`, `logs`, `login`, `bbctl`, etc.).

The bridge connects directly to Apple's IDS/APNs services. Contact Key
Verification must be disabled on the Apple account for the bridge to function.
macOS builds use native AAAbsintheContext NAC validation. Linux runtime uses a
hardware key extracted from a Mac once; there is no NAC relay process at
runtime on this branch.

The stack is:

`cmd/corten-matrix` (binary entrypoint + CLI dispatch) -> `pkg/cli` and
`pkg/connector` (management + bridge logic) -> `pkg/rustpushgo` (UniFFI FFI
wrapper) -> `third_party/rustpush-upstream` (pinned rustpush source)

## Primary Entrypoints

- `cmd/corten-matrix/`: bridge process bootstrap, setup/login helpers, Full Disk
  Access checks, and management subcommand dispatch.
- `pkg/cli/`: host-side lifecycle commands used by the `corten-matrix` binary.
- `pkg/connector/`: bridgev2 connector, message handling, backfill, contacts,
  FaceTime, StatusKit, shared albums, and privacy scrubber behavior.
- `pkg/bbctl/`: Beeper bridge-manager implementation invoked as
  `corten-matrix bbctl <args>`.
- `pkg/imconfig/`: embedded network config source of truth.
- `pkg/rustpushgo/src/lib.rs`: Rust wrapper objects and exported FFI surface
  consumed by Go.
- `third_party/rustpush-upstream.sha`: pinned upstream rustpush commit. The
  Makefile clones/prepares `third_party/rustpush-upstream` from this pin.
- `rustpush/open-absinthe/`: repo-owned overlay for the upstream open-absinthe
  crate.
- `imessage/` and `nac-validation/`: macOS native integrations.
- `tools/`: checked-in release artifacts for hardware-key extraction.

## Build And Verify

Source builds are macOS-only on this branch. The default user path is the
prebuilt release binary; local source builds are for development.

- `make` or `make build`: checks/install missing Homebrew build deps, prepares
  pinned rustpush sources, builds `librustpushgo.a`, then builds the
  self-contained `corten-matrix` binary.
- `make rust`: rebuilds only `librustpushgo.a`.
- `make bindings`: regenerates Go FFI bindings and runs
  `scripts/patch_bindings.py`.
- `make clean`: removes the binary and Rust build artifacts.

Go tests exist in targeted packages; run focused `go test` commands for packages
you touch. On macOS, plain `go test` may not find Homebrew libolm headers; match
the Makefile environment when needed:

```bash
CGO_CFLAGS="-I/opt/homebrew/include" CGO_LDFLAGS="-L/opt/homebrew/lib -L$(pwd)" go test ./pkg/connector
```

Use `/usr/local` instead of `/opt/homebrew` on Intel Homebrew installs.

Run targeted cargo tests when changing Rust behavior:

- `cargo test --manifest-path pkg/rustpushgo/Cargo.toml`
- `cargo test --manifest-path rustpush/open-absinthe/Cargo.toml`
- `cargo test --manifest-path nac-validation/Cargo.toml` when native NAC code is touched.

If you change Go bridge logic, at minimum run `make build` on macOS when
feasible. If you change the FFI boundary or Rust objects exposed to Go, run
`make bindings` and then `make build`.

## Hard Constraints

### FFI Boundary (Go <-> Rust)

Never hand-edit `pkg/rustpushgo/rustpushgo.go` or
`pkg/rustpushgo/rustpushgo.h`; regenerate them.

```bash
make bindings
make build
```

`make bindings` requires `uniffi-bindgen-go` matching UniFFI `0.25.0`:

```bash
cargo install uniffi-bindgen-go --git https://github.com/NordSecurity/uniffi-bindgen-go --tag v0.2.2+v0.25.0
```

If generated Go changes, check whether `scripts/patch_bindings.py` also needs an
update.

### Generated And Derived Artifacts

- `pkg/rustpushgo/rustpushgo.go` and `pkg/rustpushgo/rustpushgo.h` are generated.
- `librustpushgo.a`, `corten-matrix`, `.build-commit`, `pkg/rustpushgo/target/`,
  and `rustpush/target/` are build output.
- `third_party/rustpush-upstream/` is prepared by `make` from
  `third_party/rustpush-upstream.sha`; prefer repo-owned overlays/wrappers
  instead of editing cloned upstream source directly.

### Path And Toolchain Constraints

- The repo path must not contain spaces; the Makefile hard-fails because CGO and
  linker paths break.
- Top-level Go module targets Go `1.25.x` (`go.mod` uses `go 1.25.0` and
  `toolchain go1.25.11`).
- Source builds are macOS-only in this branch because native NAC uses Apple's
  AAAbsintheContext framework. Linux users run prebuilt binaries.

## Config

### Network Config

**Source of truth: `pkg/imconfig/example-config.yaml`**. It is embedded
verbatim via `//go:embed` into `NetworkExampleConfig`
(`pkg/imconfig/imconfig.go`) and consumed by both the connector and the
`corten-matrix bbctl` path. Do not re-add a hardcoded config string elsewhere.

The matching Go struct is `IMConfig` in `pkg/connector/config.go`, with
`upgradeConfig` handling migrations.

To add or change a network option, edit all three in lockstep:

1. `pkg/imconfig/example-config.yaml`: YAML key, default value, and user-facing
   comment.
2. `pkg/connector/config.go`: `IMConfig` field, `yaml:` tag, and doc comment.
   Keep the wording aligned with the YAML comment.
3. `pkg/connector/config.go` `upgradeConfig`: add a `helper.Copy(...)` line so
   the option survives config upgrades.

`pkg/imconfig/imconfig_test.go` guards that the embedded YAML stays valid.

### Privacy/Runtime Debug Config

The shipped privacy default is intentional: `debug_disable_privacy` stays
`false` unless the user explicitly asks for runtime behavior that keeps
plaintext/log values visible. On personal branches, agents may inspect local
runtime data per the note above, but that is an investigation allowance, not a
reason to flip defaults, bypass `logSafeHandle`/`logSafeURL`, or weaken the
scrubber.

### Bridge Framework Config

The `bridge:` section is bridgev2 framework config, not this connector's network
config. To change a framework default, override it in `IMConnector.Start()` after
config YAML is loaded. Keep existing overrides such as
`phone_numbers_in_profile`, `unknown_error_auto_reconnect`, and
`unknown_error_max_auto_reconnects` in mind.

## Runtime State

- Primary account state lives under `~/.local/share/corten-matrix/`; the second
  account uses `~/.local/share/corten-matrix-1/`.
- `corten-matrix logs` tails the first account; `corten-matrix logs 1` tails the
  second.
- Structured bridge logs live under each data dir's `logs/bridge.log`.
- macOS services use `launchd` with `com.lrhodin.corten-matrix`; Linux services
  use `systemd` as `corten-matrix`.
- `corten-matrix reset` is the preferred interactive reset path. For Beeper it
  rebuilds the remote registration and local bridge database by default while
  preserving Apple/iMessage identity state; use `--local-only` to keep remote
  state or `--delete-imessage-state` for the separately confirmed Apple-state
  wipe. Manual reset removes the service plus the relevant
  `~/.local/share/corten-matrix*` data dir and therefore also destroys Apple
  state, so do not use it for ordinary duplicate-room recovery.

## Reset-Risk Changes

Call out reset risk explicitly when a change affects persisted IDs or session
state. A full reset may be warranted for changes involving:

- portal ID or key format
- bridge DB schema or metadata layout
- login/session restore state
- backfill source or logic that assumes fresh portal state
- removal of legacy portal types still stored in the DB
- dual-account data-dir or service orchestration behavior

Relevant files include `pkg/connector/ids.go`, `pkg/connector/dbmeta.go`,
`pkg/connector/identity_store.go`, `pkg/cli/cli.go`, and migration-sensitive
logic in `pkg/connector/client.go`.

## Documentation Hygiene

If you change a user-facing workflow or hard engineering constraint, update docs
in the same patch:

- `README.md` for install, release, and operator-facing behavior.
- `AGENTS.md` for repo-specific agent guidance.
- `CLAUDE.md` only if that file exists on the branch and the longer internal
  engineering notes need to stay aligned.
