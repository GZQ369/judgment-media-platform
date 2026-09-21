# Publication Orchestrator

`internal/publication` is the durable transaction core for formal platform
submission. It is intentionally separate from `internal/publisher` and from
the Windows browser bridge. `cmd/server` selects the configured official
Douyin adapter or falls back to Ego. Xiaohongshu and Bilibili select Ego for
one-use submission; the Xiaohongshu official adapter only produces a client
handoff and is never selected for one-use commit.

## Contract

The package injects a `*sql.DB` and runs an idempotent migration that owns only
`pub_*` tables. The core types are:

- `Package`: `creator`, `account`, `platform`, `run`, `atomRevision`, `variantID`,
  video and cover references with their actual SHA-256 digests, and complete copy
  text or its SHA-256 digest. The fingerprint includes `VideoSHA256`,
  `CoverSHA256`, and `CopySHA256`; a path or copy summary alone is never enough.
- `Policy`: a version plus selected `douyin`, `xiaohongshu`, and/or `bilibili`.
  `DefaultPolicy` selects all three and never adds WeChat.
- `Authorization`: one-use approval bound exactly to creator, account, platform,
  run, variant, fingerprint, policy version, and expiry.
- `Adapter`: platform capability, `Commit`, and `Reconcile`. Adapters return
  `Evidence`; the core accepts only verified evidence with an external ID or
  URL (or verified rejection proof).

The normal lifecycle is:

```text
Authorize -> Prepare -> Commit -> submitted/reviewing/published/rejected
                         |
                         +-> unknown -> Reconcile -> evidence-driven state
```

`Prepare` verifies the local video and cover bytes against the package digests,
then derives the business day from the orchestrator clock in `Asia/Shanghai`.
`BusinessDay` is only an assertion and cannot choose or bypass that day. The
database unique key is `(creator, business_day)` on `pub_runs`, so a creator
can admit at most one content run per local day. Multiple platforms, accounts,
covers, and copy variants selected by one run can share that admission.

Before any adapter call, `Commit` atomically consumes the authorization and
changes the publication from `draft_ready` to `committing`, recording a
reservation token. A concurrent caller cannot obtain a second reservation.
After the adapter is called, a timeout, crash window, adapter error, or
insufficient evidence is `unknown`; `Commit` will not be retried. Only
`Reconcile` may ask the adapter for evidence after that point. A UI `click`,
`READY` message, or ordinary adapter return without verified external evidence
cannot become `submitted`.

The durable states are `draft_ready`, `committing`, `submitted`, `reviewing`,
`published`, `rejected`, `unknown`, and `failed`. The current core uses
`failed` for future definitive, pre-reservation failures; uncertain adapter
outcomes remain `unknown` by design.

## Capability boundary

`Prepare`, `Commit`, and `Reconcile` require a registered adapter whose
`Capability` reports `Configured: true`. Missing or unconfigured adapters
return an explicit `CapabilityBlocker` describing the platform and reason.
`internal/publication.NewPublisherAdapter` is the narrow compatibility wrapper
for the existing `internal/publisher.Publisher` contract. It admits only
Douyin, Xiaohongshu, and Bilibili; WeChat Channels is rejected explicitly.
Capability `Configured` is passed through from the wrapped publisher. On
commit, the wrapper maps the package media and complete copy fields to
`domain.PublishProjectRequest` and sets `Confirm: true` only after the core has
consumed its one-use authorization. This is an internal handoff, not a second
authorization path. Although the wrapper can represent Xiaohongshu's legacy
publisher contract, `cmd/server` never selects that handoff-only adapter for
one-use commit. An `XHS_SHARE_APP_KEY` alone does not enable Xiaohongshu
submission; without configured Ego, its capability remains blocked.

The legacy publisher contract has no official query operation. Consequently,
its `Reconcile` never calls `Publish`: it returns unverified evidence and leaves
the publication pending/unknown. The Ego adapter persists its opaque task-space
reference in a restricted SQLite store keyed by a hash of the stable
publication reservation and the package binding. After a restart, reconcile
uses that stored reference and never falls back to a new query lease token or
calls `publishOnce`. A client handoff such as Xiaohongshu's `action_required`
result is likewise never converted into `submitted`.

## Distribution coordination boundary

`publication.Service` is the only bridge from a durable `domain.Run` to the
transaction core. It depends on the small `RunReader` interface and accepts a
candidate only when the run is `media_ready`. It locates the durable
`publish_package.json`, the reserved rendered-video artifact
(`id=rendered-video` with `kind=video` or the legacy explicit kind), and a
durable cover artifact. The package JSON is bounded in size; media bytes and
the complete selected copy are hashed again. Client paths, media digests, and
copy summaries are never trusted.

Candidate and Prepare are read-only with respect to platform adapters. Prepare
rebuilds the candidate and compares its fingerprint before calling the existing
orchestrator. Authorization requires explicit confirmation, is bound to the
exact candidate and policy, and has a short configurable maximum lifetime.
Commit is the only path that consumes the one-use authorization and can invoke
an adapter. Missing or unconfigured official/browser capability is returned as
a capability blocker; a draft click, `READY` signal, or action-required handoff
cannot become a verified submission.

`Package` retains `Copy` as the backward-compatible caption field and now also
stores optional structured `Title` and `Tags`. These fields are persisted inside
the existing package JSON and are included in the fingerprint, so changing any
published text invalidates a prepared publication. Existing package JSON that
lacks them remains readable with empty structured fields.

## Tests

`internal/publication/publication_test.go` injects modernc SQLite connections
and local fake adapters. It covers database reopen roundtrip, concurrent
reservation, one creator daily run with three platform publications, second
same-day content rejection, Asia/Shanghai boundary handling, expired and
changed fingerprints, changed local media bytes, unknown/reconcile gating and
operation-token races, capability blockers, and rejection of unverified
`READY`-like output.

The service is wired to Distribution's durable `media_ready` runs. Real
submission requires configured Ego/browser capability for Xiaohongshu and
Bilibili, or configured official Douyin credentials or Ego for Douyin. The
repository tests use fake runners only; no test performs
a real browser action or platform submission. Account OAuth, platform
callbacks, and analytics remain outside this component.
