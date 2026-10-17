# Engineering Invariants

Contracts for identity, history, persistence, services, and publication. These
apply across creation, restart, retry, and restore. See [README](../README.md)
for operator workflows and [AGENTS](../AGENTS.md) for editing and verification
commands.

Changes to persisted IDs, metadata, sessions, or account/service layout must
make any migration/reset requirements explicit.

## Escrow bottle recovery

Preserve upstream's escrow and named-peer signatures over the exact bottle.
Recovered-key equality cannot replace peer authorization; invalid signatures
must fail without searching other peers for a bypass. Source preparation must
remove the retired held-keys override before reapplying retained patches.

## Portal identity and transport identity

A portal key is stable room identity; a delivery handle is transport identity.
Contact canonicalization preserves these distinctions:

- Inspect all aliases for existing portals first. Prefer a populated room and
  preserve its exact stored key, including legacy spelling/case. For new
  contacts, choose sorted normalized phone identifiers, then sorted emails.
- On lookup/enumeration failure, retain the incoming ID and keep sync retryable.
  Exclude self chats from ordinary alias merging.
- Keep portal ID, DM `OtherUserID`, members, sender mapping, and backfill key
  consistent. Merge histories by full `networkid.PortalKey`, including `Receiver`;
  never collapse logins.
- Store the concrete carrier recipient in `PortalMetadata.SMSDestination`.
  Sending and carrier repair must use it without re-keying the portal; derive
  transport from portal identity only for the explicit legacy metadata fallback.

Alias ordering and concurrent first messages must produce the same canonical
portal choice.

## Exact chat.db GUIDs and backfill

`PortalMetadata.ChatDBGUIDs` holds literal Apple GUIDs, including suffixes such
as `(smsft)`. Normalized portal identifiers cannot reconstruct them.

- Eligibility, fetching, initial sync, and `restore-chat` use the same GUID set.
- Refresh existing-room metadata by union only: no room creation, re-keying,
  deletion of saved GUIDs, or automatic backfill. Failed enumeration/save stays
  retryable with prior metadata intact; an incomplete pass cannot be marked done.
- Refresh every forward fetch and the first backward page (`Cursor == ""`),
  never midway through backward pagination. Empty legacy metadata retains the
  contact-aware lookup across `any`, `iMessage`, and `SMS` variants.
- Deduplicate by message GUID, including duplicates across exact GUIDs,
  backward pages, forward fetches, and live APNs delivery.

## Current SMS/iMessage routing

The newest known chat.db state determines the current delivery service;
older merged GUID histories contribute backfill only.

- Never OR historical SMS values or infer current service from `ChatDBGUIDs`.
- Persist both transitions: SMS sets `IsSms` and `SMSDestination`; iMessage
  clears both. Seed chat.db routing only after canonicalization and successful
  existing-room inspection; newer live APNs routing in the same session wins.
- Concurrent route and GUID persistence must not overwrite each other's updates.
  Save failure preserves the newer runtime route and retryable portal metadata.

## CloudKit eligibility and scrubbing

`bridge_filtered_chats` defaults off. Readers, reports, and scrub/backfill gates
must share eligibility rules:

- Classify known source chats by `cloud_message.chat_id`: filtered/deleted
  sources fail closed without suppressing an unfiltered sibling's history.
- Empty/unrecognized source IDs retain portal-level compatibility only when
  every live sibling is unfiltered; synthetic/recycle metadata must not disable it.
- A known source remapped to another portal cannot authorize history through
  the old portal. Keep it unreadable but preserve plaintext for authoritative
  re-ingest, distinct from permanently excluded content.
- Scrub candidates and writes stay scoped to login, receiver, and portal.
  Ordinary delivered-body scrubbing requires exact message IDs and portal-scoped
  Matrix delivery proof valid at the time of the write; a prefetched set is
  only a prefilter.
  Deleted witnesses and remapped sources cannot authorize a later chunk.
- Pending initial backfills and active or concurrently starting restores retain
  required plaintext. Permanently filtered/deleted rows remain eligible for
  cleanup independently of those holds.
- Forward-backfill cancellation releases acquired capacity and bootstrap
  accounting, including when cancellation races acquisition. Unfinished history
  remains recoverable; retries cannot outlive their portal/client lifecycle.

## Sync-status diagnostic contract

`pkg/connector/sync_status.go` serves the management room and host CLI. It reads
persisted state even with the daemon stopped; it must not migrate, create a
missing supported Beeper `file:` SQLite database, or write progress counters.

Match reader eligibility, content, ordering, and cap windows: exclude filtered
and deleted siblings before ranking the cap; readable reaction/system rows
still consume slots as in `listLatestMessages`. Report source remaps separately
from Unknown Senders filtering. Never render persisted CloudKit errors, database
URIs, Apple handles, record IDs, or driver details. Host reports must show live
sync/batch-send capabilities as unknown.

## Duplicate inserts and reset safety

Do not suppress bridge-message uniqueness errors with `INSERT OR IGNORE`.
Matrix may accept a batch before its local record is inserted; hiding a collision
can leave history untracked and break edits, reactions, and receipts.
Recovery must distinguish duplicate alias history from unique missing or
already tracked history. An empty room alone proves neither damage nor a need
to reset. Never automatically merge, re-ID, delete, leave, or archive populated
rooms.

Reset implementation must preserve these boundaries:

- Explicitly confirm recovery; preserve Apple identity by default. Interactive
  `--delete-imessage-state` requires separate confirmation. Enumerate known
  disposable artifacts so unknown files and config backups survive.
- Refresh `session.json` atomically at shutdown, sync file and parent directory,
  and wait before disconnect completes. Validate against the keystore; CloudKit
  additionally needs restorable token-provider credentials, MobileMe delegate,
  and trust-circle state. A valid previous atomic backup remains usable if the
  best-effort final save fails; do not require a freshness proof a retry bypasses.
- Reject unsupported providers, account/database layouts, and unknown options
  before stopping. Keep the primary-account/default-SQLite Beeper flow and
  fixed `sh-imessage` registration name; broader orchestration is separate work.
- Require an error-aware stopped-process check and bounded shutdown before
  deletion. Beeper deletion failure preserves local bridge state; verified
  not-found is already success in `bbctl delete`.

Live investigation and mutation authorization rules are in
[AGENTS](../AGENTS.md#local-investigation).

## Management rooms

Management rooms are replaceable; bot-created rooms may be replaced with
user-owned rooms when double-puppet access is available. FaceTime, Shared
Streams, and recycle-bin notices must respect an intentionally cleared room
pointer rather than recreate the room. Repair missing default names/avatars in
place and preserve custom values. Management-room state is disposable.

## Service supervision and unit ownership

`bridge-all` supervises accounts independently: restart only the failed child.
Service shutdown instead signals all children, bounds graceful shutdown,
kills only after timeout, and reaps all children before exiting. Release parent
stdout-log descriptors after every child exit or failed start; log sanitized
errors and account indices rather than full data paths.

On Linux, resolve scope from installed units, not just bus reachability:

- Duplicate user/system units block setup/install/start/restart until the
  operator removes one; never delete or disable a duplicate automatically.
  Stop attempts every installed scope and reports failures.
- An existing user unit with an unreachable manager blocks setup's system-scope
  fallback. Uninstall inspects and verifies both scopes.
- `install-service` overwrites only managed units, preserving runtime identity
  and the XDG data directory. Reset reuses scope-aware stop verification.

## Branch and stacked-PR publication

`main` mirrors upstream master; active automation belongs on `beta-latest`.
Stacked PR heads and bases must remain consistent after rewrites.

Publish rewritten stacks with one atomic push and explicit leases:
`--force-with-lease=<branch>:<expected-old-sha> --atomic`. Read back remote heads
and PR bases. Lease mismatch means fetch and re-review, not overwrite.

## Release workflow provenance

Source builds compile the reviewed tree; do not rewrite tracked Go/config files
with build-time patches. Prepared dependency patches belong in the Makefile or
`third_party/patches/`.

Release CI is manual-only, public actions stay pinned, and default permissions
remain read-only. Preflight resolves one private OpenCider revision;
every build/finalizer checks out that revision through its opaque token. Never
print private commit metadata or compiler transcripts. A fresh full run may
select a newer revision; an unresolvable token must fail rather than drift.
Only packaging gets release-write permission, without the private key.

Tags must point directly to the dispatched public commit. Serialize packaging
by resolved tag and verify the ref immediately before and after draft creation.
These checks are not a lock against other privileged tag writers: publication
remains a manual boundary. Attach only the three intended binaries and never
replace existing release assets. Manual versions must work without a prior
published release. Verification commands are in [AGENTS](../AGENTS.md#verification).
