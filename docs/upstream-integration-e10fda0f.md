# Upstream integration: `e10fda0f`

This integration brings 12 release-CI commits from upstream `main` into
`beta-latest`. It does not change bridge runtime code or approve deployment.

## Provenance and scope

- Previous beta: `6a506d86bcf24880d4017e6f1fdc23533533cfde`.
- Previous integrated upstream: `5758f5970062a0add9ffb93b65c985247fe0397a`.
- New upstream: `e10fda0fb65a6931ee1f65d8ab4b392e8db34b73`.
- The fork's `origin/main` and actual upstream `master` matched at inspection.
- The only textual conflict was the release-binary workflow.

Upstream adds platform selection, semantic-version resolution, patch
verification, private checkout and build-log containment, executable-mode
restoration, and artifact boundary tests. Both macOS slices build on Apple
Silicon, with a native Intel smoke test before universal assembly. Linux
builds use native Ubuntu runners and the private Zig build entrypoint.
Only packaging receives release-write permission; private builds do not.

## Intentional integration choices

Upstream's opaque builder-token mechanism replaces the previous public raw-SHA
input and summary. Preflight resolves private `master` once, and every builder
and the finalizer check out that exact revision through the token. This keeps
private revision metadata out of public logs. It guarantees a consistent
builder within a run; a fresh full run can select newer private `master`.
If an old token cannot be resolved, checkout fails rather than silently using
a different revision. No new cross-run attestation system is added.

The workflow differs from upstream in three small areas:

- Manual version selection skips the latest-release API. The fork had no
  published releases at inspection, so the upstream unconditional lookup
  would block a manual first release.
- Existing tags must point directly to the dispatched public commit for both
  manual and automatic versions. Upstream's manual exception accepted tags
  pointing elsewhere, which could label binaries with the wrong source.
  Same-commit retries remain allowed. The ref is checked immediately before
  draft creation and again afterward.
- Packaging jobs are serialized by the resolved release tag. Platform builds
  may overlap. This retains beta's protection around release mutations without
  serializing unrelated build work.

Tag checks cannot prevent another privileged writer from moving a tag between
checks. Releases remain drafts for manual review and publication; existing
release assets are never overwritten. All helper scripts and artifact
allowlists are retained from upstream.

## Validation and limits

Validation covers shell syntax, workflow linting, semantic-version behavior,
and the workflow safety contract. Synthetic GitHub responses exercise first
release selection, tag collisions, same-commit retries, reservation races, and
tag movement around draft creation. Helper checks cover exact artifact lists,
symlink rejection, hidden private output, cancellation, and temporary-file
cleanup. An additional local synthetic Git repository confirmed opaque tokens
can select the exact older revision after fetching master.

The actual private OpenCider builds, cross-platform runner execution, artifact
inspection against real release binaries, and GitHub tag/release mutations
remain untested here. No release workflow was dispatched. Since no Go, Rust,
FFI, Makefile, or runtime code changed, no local bridge rebuild was required.

No portal IDs, session state, backfill state, schema, or reset behavior changed.
No reset or re-login is required. The running bridge, staged executable,
launchd service, Apple state, and runtime databases were left untouched.
