# Project Guide

Read `README.md` for setup and source layout. Read `docs/ENGINEERING.md` before
changing identity, routing, backfill, persisted state, reset, service supervision,
escrow recovery, or branch/release workflows.

## Local investigation

- Start live-bridge investigations read-only. Stopping, restarting, deploying,
  resetting, or mutating live state requires explicit user authorization.
- On non-main personal branches, raw local logs, config, and database values may
  be inspected. Keep account data, secrets, raw artifacts, and account-derived
  examples out of commits and handoff notes; use synthetic data.
- This allowance does not authorize changing privacy defaults, sanitizers, or
  scrubbers. Keep `debug_disable_privacy: false` unless explicitly requested.

## Editing

- Never hand-edit `pkg/rustpushgo/rustpushgo.go` or `pkg/rustpushgo/rustpushgo.h`.
  Run `make bindings`, then `make build`. Check `scripts/patch_bindings.py`
  when generated Go changes. `make bindings` needs the generator matching
  UniFFI 0.25.0:

  ```bash
  cargo install uniffi-bindgen-go --git https://github.com/NordSecurity/uniffi-bindgen-go --tag v0.2.2+v0.25.0
  ```
- `third_party/rustpush-upstream/` is prepared from
  `third_party/rustpush-upstream.sha`. Change the pin, Makefile patches, or
  repo-owned overlays/wrappers instead of the prepared tree. `corten-matrix`,
  `librustpushgo.a`, `.build-commit`, and Rust `target/` directories are build
  output; `tools/*.zip` are tracked release assets.
- Keep network options aligned across `pkg/imconfig/example-config.yaml`,
  `IMConfig`, and `upgradeConfig` in `pkg/connector/config.go`. The embedded YAML
  is the default config source of truth. Override framework `bridge:` defaults
  in `IMConnector.Start()` after loading YAML.

## Verification

- Full source build: `make build` (macOS, checkout path without spaces).
  Building does not deploy or restart the running bridge.
- Go tests may need the Makefile's Homebrew paths for CGO (`/usr/local` on Intel):

  ```bash
  CGO_CFLAGS="-I/opt/homebrew/include" CGO_LDFLAGS="-L/opt/homebrew/lib -L$(pwd)" go test ./pkg/connector
  ```

- Rust tests: `cargo test --manifest-path <crate>/Cargo.toml` for each touched
  crate (`pkg/rustpushgo`, `rustpush/open-absinthe`, `nac-validation`).
- Release CI checks: `bash scripts/tests/next-release-tag-test.sh` and
  `bash .github/workflows/tests/build-release-binaries-contract.sh`.
  These use synthetic inputs and do not validate private builds.
