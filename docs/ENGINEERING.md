# Engineering Invariants

This document records data-integrity and release rules that must remain true
when changing portal identity, chat.db backfill, SMS routing, reset behavior, or
stacked pull requests. Treat the rules as lifecycle contracts: a focused test
that bypasses creation, restart, retry, or restore behavior is not sufficient.

## Portal identity and transport identity

A Matrix portal key is stable room identity. The handle used for the next
iMessage or SMS delivery is transport identity. They may differ after contact
aliases are canonicalized, so never derive one from the other unless the
relevant metadata is absent and the legacy fallback explicitly applies.

- Use the shared contact policy in `pkg/connector/contact_merge.go`. For a new
  multi-handle contact, select a deterministic normalized handle: sorted phone
  identifiers first, then sorted email identifiers.
- Before choosing a fresh canonical ID, inspect every alias for an existing
  portal. Prefer a populated existing room and preserve its exact stored key,
  including legacy spelling or case. Do not re-key an existing portal merely to
  normalize it.
- Fail closed on portal lookup or enumeration errors. Retain the incoming
  portal ID and leave initial sync retryable instead of guessing a canonical
  identity.
- Keep self chats out of ordinary contact-alias merging.
- Keep the portal ID, DM `OtherUserID`, member map, sender canonicalization, and
  backfill portal key consistent.
- Store a canonical DM's concrete carrier recipient separately in
  `PortalMetadata.SMSDestination` (`pkg/connector/dbmeta.go`). Sending and
  carrier-repair code must use this transport destination while preserving the
  stable portal ID.

When changing this area, test deterministic choice with reordered aliases,
concurrent first messages on different aliases, existing populated and empty
rooms, legacy spellings, self chats, and lookup failures. Also test inbound
sender mapping and outbound destination selection through the same portal.

## Exact chat.db GUIDs and backfill

`PortalMetadata.ChatDBGUIDs` is the authoritative set of literal chat.db GUIDs
for a portal. Preserve complete strings, including Apple suffixes such as
`(smsft)`: normalized portal identifiers cannot reconstruct those values.

- Use the same literal GUID set for backfill eligibility and message fetching.
  Initial sync and `restore-chat` must pass it in
  `ChatResync.BundledBackfillData`.
- Merge histories by full `networkid.PortalKey`, including `Receiver`, and
  union/deduplicate their exact GUIDs. Never collapse state across logins.
- Exact-GUID refresh is metadata repair for existing rooms only. It must not
  create rooms, re-key portals, start backfill, delete stale entries, or replace
  the saved set. Add newly discovered matching GUIDs by union.
- A failed enumeration or save must remain retryable. Preserve the old
  in-memory metadata on save failure and do not write a completion marker that
  hides an incomplete pass.
- Portals without exact metadata retain the contact-aware legacy lookup across
  `any`, `iMessage`, and `SMS` variants.
- Refresh at a real backfill session boundary: every forward fetch and only the
  first backward page (`Cursor == ""`). Do not change the GUID set in the
  middle of backward pagination.
- Deduplicate merged chat.db messages by message GUID. Keep bridge-level
  `AggressiveDeduplication` enabled for forward fetches and multi-GUID backward
  sessions, where a duplicate may cross pages or already exist from live APNs.

The primary implementation and tests are in `pkg/connector/chatdb.go`,
`pkg/connector/chatdb_guid_refresh.go`,
`pkg/connector/chatdb_test.go`, and
`pkg/connector/chatdb_guid_refresh_test.go`.

Verification must cover suffix-only GUIDs, a new exact variant discovered
between sessions, an unchanged active backward cursor, union-only persistence,
save rollback and retry, restore bundling, shared rows visible through multiple
GUIDs, and legacy empty metadata.

## Current SMS/iMessage routing

The ordering in `imessage/mac/messages.go` makes
`GetChatsWithMessagesAfter` deterministic and newest-first. When several
literal GUID histories merge into one portal, the first representative defines
the current service and later GUIDs contribute backfill history only.

- Never infer current `IsSms` by OR-ing historical GUIDs or by scanning
  `ChatDBGUIDs` for any SMS value. That would turn an SMS-to-iMessage upgrade
  back into SMS after restart.
- Persist both transitions: `IsSms=true` with the concrete
  `SMSDestination`, and `IsSms=false` with the destination cleared.
- Seed chat.db routing only after canonicalization and successful existing-room
  inspection. A newer live APNs route observed in the same session wins.
- Serialize current-route persistence and exact-GUID metadata persistence with
  `IMClient.smsRoutePersistMu`. On save failure, retain the newer runtime route
  and restore retryable portal metadata rather than durably reapplying stale
  state.

Relevant behavior lives in `pkg/connector/client.go`,
`pkg/connector/carrier_route.go`, and
`pkg/connector/chatdb_guid_refresh.go`. Test SMS-to-iMessage and
iMessage-to-SMS transitions through live delivery, persistence, restart,
initial sync, restore, concurrent GUID refresh, and failed-save retry.

## Duplicate inserts and recovery safety

A duplicate bridge-message insert is a data-integrity signal, not a harmless
SQLite nuisance. Do not use `INSERT OR IGNORE` or otherwise suppress the
message uniqueness error as a repair for duplicate portal/backfill inserts. A
Matrix batch can be accepted before the local message row is recorded, so a
hidden collision can leave room history untracked and break edits, reactions,
or read receipts.

Diagnose before repairing:

1. Correlate each error's message ID, part ID, portal key, receiver, and time
   with the surrounding bridge logs.
2. Query portal and message rows to determine whether the same event belongs to
   another alias portal and whether Matrix accepted the corresponding batch.
3. Classify the result as duplicate alias history, unique missing history, or
   already tracked history. Do not infer damage from an empty room alone.
4. Reproduce with synthetic fixtures and fix the identity or lifecycle cause.
   Do not automatically merge, re-ID, delete, leave, or archive populated
   Matrix rooms.

Raw local logs and database values may be inspected on an authorized personal
debug branch, but must remain uncommitted. Never stop, reset, edit, or otherwise
mutate a live bridge, Beeper registration, Matrix room, or local state directory
without explicit authorization.

Use `corten-matrix reset` only as an explicitly confirmed recovery operation.
Its default contract preserves Apple/iMessage identity while rebuilding
disposable bridge state. `--delete-imessage-state` is exceptional and requires
its separate confirmation in interactive use. Default cleanup must enumerate known
disposable artifacts rather than enumerate Apple state: unknown future files
survive. Keep the upstream fixed registration name (`sh-imessage`), refresh
`session.json` atomically during final shutdown, wait for that save before
disconnect completes, validate the saved session against its keystore, and
require trust-circle state when the config selects CloudKit backfill. Verify the
bridge process stopped with a required, error-aware process check and a bounded
shutdown wait before deletion. When the bridge was running, require proof that
its atomic shutdown save replaced `session.json`; an older valid backup is not
a fresh export. A Beeper deletion error must leave local bridge state intact;
`bbctl delete` already treats verified not-found endpoints as success. Reject
unknown options, self-hosted installs, PostgreSQL, and custom SQLite paths
before stopping the service. Preserve setup-created config backup files during
the default cleanup.
Keep these boundaries covered by behavior tests in `pkg/cli/reset_test.go`.
The command intentionally targets the upstream primary-account/default-SQLite
flow; broader database or account orchestration is a separate feature.

## Service supervision and unit ownership

`corten-matrix bridge-all` is the single service entrypoint for both configured
accounts. It supervises them independently: one account exiting must restart
only that account while the other continues running. Service shutdown is the
opposite boundary and must be coordinated across all children: propagate the
signal, bound graceful shutdown, kill only after the timeout, and reap every
child before the supervisor exits.

- Release the parent copy of each account's `bridge.stdout.log` descriptor on
  every successful exit and failed start so a crash loop cannot exhaust file
  descriptors.
- Keep supervisor diagnostics useful without logging full account data paths.
  The account index and sanitized process error are sufficient.
- On Linux, resolve service scope from where the unit actually exists, not only
  from user-bus reachability. A system unit can coexist with a reachable user
  bus.
- `install-service` may overwrite only units bearing its managed marker and
  must preserve their runtime identity and XDG data directory. Uninstall must
  inspect and verify both user and system scopes.
- Reset reuses scope-aware stop detection after confirmation and must verify
  that the bridge process stopped before deleting state.

The primary implementation and lifecycle tests are in `pkg/cli/cli.go`,
`pkg/cli/supervisor_test.go`, `pkg/cli/unit_test.go`, and
`scripts/reset-bridge.sh`.

## Branch and stacked-PR publication

`main` is a clean mirror of upstream master. Active repository automation,
including `.github/workflows/codex-review-on-ready.yml`, belongs on
`beta-latest`. Verify the repository, default branch, base branch, and remote
refs before publishing; do not assume a nearby fork has the same branch names.

For stacked pull requests:

1. Fetch and prune the remote, then record every current base and head SHA.
2. Reconstruct the actual dependency graph from those refs. Review each PR's
   own diff and its upgrade, reset, restore, retry, and cross-stack behavior.
3. Integrate parent fixes into dependents in dependency order and resolve shared
   code semantically. Re-run focused tests for every rewritten head.
4. Publish the complete validated stack with one atomic push and an explicit
   lease per rewritten branch:
   `--force-with-lease=<branch>:<expected-old-sha> --atomic`.
5. Read back all remote heads and PR bases. Treat any lease mismatch as new
   remote work: stop, fetch, and re-review instead of overriding it.

Before delivery, run focused package tests for changed behavior, the applicable
build described in `AGENTS.md`, `git diff --check`, and a final review that no
real account data, logs, credentials, or machine-local artifacts entered the
patch.
