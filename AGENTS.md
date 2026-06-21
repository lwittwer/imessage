# Dev notes

## ⚠️ Privacy / PII — READ FIRST (this is a PUBLIC repo)

`lrhodin/corten-matrix` is **public on GitHub**. Anything you commit — code,
**comments**, **commit messages**, test fixtures, docs — is public the moment it's
pushed, and **stays** public: redacting later needs a history rewrite + force-push,
and GitHub still retains the old commit via its commit-URL/API caches, forks, and
PR refs. **Prevent, don't fix** — treat any leaked value as permanently compromised.

This has already bitten us: a **real phone number**, used as a "malformed vCard"
example *in a code comment*, shipped to public master and survived a
commit-message redaction (it had to be scrubbed from the source separately).

**Never put real personal data anywhere in the repo.** Redact ALL of:
- **Phone numbers** (real ones — even as examples in comments/messages)
- **Email addresses** (real ones), **real names** (contacts, family, the user, the
  upstream author), **physical/mailing addresses**
- **Apple IDs / account handles**, real **iMessage/CloudKit GUIDs**, portal/chat
  IDs, record names, or push topics tied to a real account
- **Device identifiers**: serial number, MLB, ROM, UDID/UUID, IMEI, machine-id,
  `X-Apple-I-MD-M`, `X-Mme-Device-Id`, hardware-key `_enc` values
- **Secrets/identity material**: validation data, anisette, push/PET tokens,
  keychain identifiers, `client_secret`, OTP/`ptm`/`tk`
- **Raw vCard / address-book content** from a real account (test fixtures must be
  fully synthetic)

**Where it sneaks in — check every one before committing:** code comments (the one
that bit us), commit messages, test fixtures (`.vcf`/`.vcard`, golden files),
example configs, **debug logs** (`eprintln`/`info!`/`warn!`/zerolog), docs/handoff
markdown.

**Use placeholders instead:** phone `(XXX) XXX-XXXX` or `+15551234567` (555 is
reserved); email `user@example.com`; name `Jane Doe`; IDs/GUIDs obviously fake
(`AAAA…`, `00000000-0000-0000-0000-000000000000`).

**Logging:** never log raw handles/numbers/emails/URLs/device-IDs. Route them
through the existing sanitizers — `logSafeHandle`, `logSafeURL` (`pkg/connector/`)
— which is why those helpers exist.

**Before every commit:** scan the diff for real PII — `git diff` and grep for
phone patterns (`\([0-9]{3}\)`, `[0-9]{3}-[0-9]{4}`), `@` emails, serial/MLB/UDID,
real names. If you used real data to debug, keep it **local and uncommitted**;
never stage it. If PII does land in a commit, tell the maintainer immediately —
a forward commit cleans the tree, but full removal needs `filter-repo` +
force-push and may persist in GitHub caches/forks regardless.

## FFI boundary (Go ↔ Rust)

**Never hand-edit** `pkg/rustpushgo/rustpushgo.go` or `rustpushgo.h`. Always regenerate.

### Linux / macOS

```bash
make bindings   # requires uniffi-bindgen-go on PATH
make build
```

Install `uniffi-bindgen-go` (must match UniFFI 0.25.0 as pinned in `pkg/rustpushgo/Cargo.toml`):
```bash
cargo install uniffi-bindgen-go --git https://github.com/NordSecurity/uniffi-bindgen-go --tag v0.2.2+v0.25.0
```

### Windows

The Makefile's `bindings` target hardcodes UNIX static-lib naming
(`librustpushgo.a`). MSVC Rust emits `rustpushgo.lib` instead, so `make
bindings` on Windows can't find the artifact. Use the Windows-native helper
scripts under `dev/` instead:

```cmd
dev\windows-dev-env.bat && dev\windows-bindings.bat
```

- `dev\windows-dev-env.bat` — one-shot environment bootstrap: vcvarsall,
  LIB/INCLUDE, PATH (cargo, Strawberry Perl, CMake, Python, protoc, LLVM),
  LIBCLANG_PATH, `python3` shim, and a first-run `cargo install` for
  `uniffi-bindgen-go`. Idempotent — safe to re-run.
- `dev\windows-bindings.bat` — mirrors `make bindings`: builds the crate if
  the `.lib` is missing, runs `uniffi-bindgen-go` against `rustpushgo.lib`,
  then runs `python3 scripts/patch_bindings.py`. Respects `CARGO_TARGET_DIR`
  if set (so users with cloud-sync folders can redirect cargo output
  elsewhere to avoid file-lock races on openssl-sys intermediates).

The one-time winget installs needed for the dev-env script to work are
documented at the top of `dev\windows-dev-env.bat`.

## Config

### Network config (the `network:` / iMessage settings)

**Source of truth: `pkg/imconfig/example-config.yaml`** — the documented
defaults and comments users see. It is embedded **verbatim** via `//go:embed`
into `NetworkExampleConfig` (`pkg/imconfig/imconfig.go`); it is **not** generated
from Go, and there is no separate template. Both `pkg/connector` and `cmd/bbctl`
consume this one embedded copy, so **never re-add a hardcoded config string**
elsewhere — the two generation paths drifted once, which is exactly why this was
unified into `pkg/imconfig`.

The matching Go struct is `IMConfig` in `pkg/connector/config.go`, with
`upgradeConfig` handling migrations.

To add or change a network option, edit all three in lockstep:
1. `pkg/imconfig/example-config.yaml` — the YAML key, default value, and the
   user-facing comment.
2. `pkg/connector/config.go` — the `IMConfig` struct field (+ `yaml:` tag) and
   its doc comment. Keep this comment's wording in sync with the YAML comment.
3. `pkg/connector/config.go` `upgradeConfig` — add a `helper.Copy(...)` line so
   the option survives a config upgrade.

`pkg/imconfig/imconfig_test.go` guards that the embedded YAML stays valid YAML.

### Bridge (base/appservice) config — the `bridge:` section

This is the **bridgev2 framework's** config (`bridgeconfig.BridgeConfig`), not
ours. To change a framework default, override it in `IMConnector.Start()`
(config YAML is loaded by then) — see the existing overrides there for
`phone_numbers_in_profile` (must be true or bridgev2 strips `tel:` from contact
profiles → no call button), `unknown_error_auto_reconnect`, and
`unknown_error_max_auto_reconnects`.
