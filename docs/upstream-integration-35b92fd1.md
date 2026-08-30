# Upstream integration record: `35b92fd1`

This record explains the 2026-08-29 merge of upstream `main` into
`beta-latest`, including the small places where the final tree intentionally
differs from upstream. It is an integration and validation record, not a live
deployment approval.

## Scope and provenance

- Merge commit: `35b92fd1268fe45438af7c18d241eac9a31efbed`
- Previous `beta-latest`: `e2f2ea1186b797af97f7251338ae365fc508ed90`
- Integrated upstream `main`: `5758f5970062a0add9ffb93b65c985247fe0397a`
- Common base: `755e2d212c78bf76933e84d2b912c173291ccb91`

Upstream contributed management-room lifecycle changes, portable Homebrew
dependency discovery, privacy-scrubber and forward-backfill contention work,
and a manual release-binary workflow.

The management-room history contains two reverted experiments. `729991df` and
`018da71a` were reverted by `3e54ab55`; the standalone DM-marking change in
`4427c680` was reverted by `dab265df`. The effective management-room changes
are `d41878e9` and `7b3af506`.

## Integrated behavior

### Management rooms

The final management-room implementation is unchanged from upstream. New rooms
are created as the Matrix user when double-puppet access is available. Existing
bot-created rooms can be replaced, the replacement is marked as a DM, and the
welcome message is posted there. Without double-puppet access, the existing
bridgev2 fallback remains in use.

An intentionally cleared management-room pointer is respected by the FaceTime,
Shared Streams, and recycle-bin notice paths, so those paths do not immediately
recreate a room the user left.

This state is deliberately treated as disposable. The integration does not add
a migration state machine, notice replay queue, welcome retry subsystem, or
room-history preservation contract. Bridgev2's own status-notice path may still
recreate a missing room, and transient notices may be skipped while no room is
configured. Those consequences do not justify fork-specific lifecycle
machinery for a replaceable management room.

### Source builds

The architecture-aware `BREW_PREFIX` work was retained, including support for
both Apple Silicon and Intel Homebrew layouts.

Upstream's build-time patches for the omnisette provider and
`disable_icloud_contacts` were not retained. The provider adaptation already
exists in tracked beta source, while the contacts patches would rewrite tracked
Go and YAML files during the build. A source build should compile the reviewed
tree rather than leave a dirty tracked-source variant through anchor-sensitive
Perl substitutions.

### Privacy scrubber and forward backfill

Upstream's core scrubber contention improvements were retained: an index-backed
finite candidate pass, one delivered-message materialization as a prefilter,
and chunked updates. Each chunk rechecks its exact bridgev2 message IDs and
portal IDs inside the update, so the prefilter is not treated as durable proof.
Forward backfills retain the cancellable semaphore, but not the delayed retry
that upstream attached to cancellation.

The implementation was adapted to preserve beta's existing data contracts:

- Candidate and write-time predicates remain scoped by login and receiver.
- Filtered and deleted sibling chats cannot authorize or suppress the wrong
  portal's history.
- Ordinary delivered-body scrubbing requires the CloudKit source to remain
  mapped to the candidate portal, so a known remap retains plaintext for
  authoritative re-ingest.
- Permanently filtered/deleted candidates remain distinct from candidates that
  require proof of Matrix delivery.
- Matrix delivery proof is portal-scoped and revalidated in the same statement
  that scrubs the body; a deleted witness cannot authorize a later chunk.
- Pending initial backfills and active restore pipelines remain excluded.
- Restore lifecycle changes are serialized across candidate selection and the
  scrub update, preventing a restore from starting inside that window.
- Cancellation that races a successful semaphore send returns the slot.
- Cancellation ends the current attempt and releases its bootstrap accounting.
  The context belongs to the portal lifecycle, so cancellation means portal
  deletion or bridge shutdown rather than a semaphore timeout. Unfinished
  history remains eligible for the existing startup reconciliation and
  backfill-task recovery paths.

The scrubber protections are not speculative recovery layers: removing them
could permanently scrub recoverable message content. The implementation uses
the existing restore mutex and SQL eligibility predicates rather than new
persisted state. Conversely, the delayed retry was removed because enqueue
acceptance does not prove dispatch, its forced resync can be dropped before
`FetchMessages`, and its timer can cross client generations. Making that retry
safe would require lifecycle and ownership machinery disproportionate to a
teardown-only cancellation path.

### Release workflow

Upstream's manual-only workflow, pinned public actions, read-only default
permissions, private build-log containment, and exact three-artifact release
allowlist were retained.

The final workflow additionally:

- requires one exact 40-character OpenCider commit for every private checkout;
- serializes runs targeting the same release tag;
- checks immediately before release creation that the tag does not exist;
- verifies that the created tag resolves to the dispatched public commit; and
- records the private builder commit in the job summary.

Together these enforce one reproducibility rule: the tag identifies the public
source commit, while the workflow run records the requested immutable private
builder SHA. No broader attestation, checksum, publishing, or release-management
subsystem was added. The absence check and later verification are not an atomic
lock against another privileged writer creating or moving the tag; publishing
the draft remains the manual approval boundary.

## Reset and deployment impact

No portal IDs, Apple identity, login/session representation, or reset boundary
changed. No reset or re-login is required by this integration. The scrubber
schema addition is an index and is created through the existing schema setup.

Management-room replacement may delete the old bot-created room as upstream
intends. This does not affect Apple state, message portals, backfill history, or
credentials.

## Validation performed

The merged tree passed:

- focused and full `pkg/connector` tests;
- focused race-detector tests and the full connector race suite;
- deterministic multi-chunk witness-deletion and portal-remap regressions;
- broader Go tests for the connector, CLI, bbctl, command, and macOS packages;
- `go vet` across those packages;
- workflow YAML and trigger/permission inspection;
- `git diff --check` and clean-worktree checks; and
- an incremental macOS `make build` at source merge `35b92fd1`, confirmation
  that `.build-commit` matched that merge, and `codesign --verify`.

A final simplification review also confirmed that the management-room sections
are byte-identical to upstream and that every functional deviation protects a
specific data-integrity, privacy, build-reproducibility, or release-provenance
contract.

## Validation not performed

The following still require their real environments:

- management-room creation and migration on a live Matrix homeserver;
- live Apple IDS/APNs/CloudKit traffic and real-account restore behavior;
- live PostgreSQL execution;
- a forced clean Rust rebuild and execution on an Intel Homebrew host;
- the private OpenCider and cross-architecture GitHub Actions builds; and
- draft-release creation and tag verification on GitHub.

No live bridge, Matrix room, Apple state, launchd service, or local runtime
database was changed while preparing this integration.
