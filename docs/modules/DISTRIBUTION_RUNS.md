# Distribution Runs

The asynchronous run API is backed by the SQLite ledger at
`<data-dir>/runs/runs.sqlite`.

## Lifecycle

`POST /api/v1/runs` validates and durably queues a run before returning `202`.
Clients can poll `GET /api/v1/runs/{id}` or consume
`GET /api/v1/runs/{id}/events`. `cancel`, `resume`, and `retry` are exposed as
POST actions on the same run resource. An idempotency key with the same JSON
payload returns the existing run; reusing it with a different payload returns
`409`.

Offering runs produce a local `publish_package.json` artifact and finish at
`package_ready` unless the optional request `media.enabled` is true. Media
runs first record that package, then emit `media.validating`, `media.started`,
one `media.artifact` per manifest artifact, and `media.ready`, finishing at
`media_ready`. A media-ready run means the worker manifest was accepted; its
metadata sets `video_ready` only for the explicit rendered-video artifact
(`id`=`rendered-video` with `kind`=`video`; the legacy explicit kind is
retained for compatibility).
Audio, subtitle, preview, or other generic video artifacts are not a rendered
video. Validated research and judgment atoms are adapted into a
capability/content workflow input: their viewpoint, insight, counterpoint,
audience, CTA, and evidence IDs are retained without being presented as
product selling points. They do not trigger another research or judgment
stage. Bilibili uses the deterministic `bilibili_knowledge_story` template
and is package-ready without a publishing integration. New WeChat Channels
runs are disabled. Existing legacy project and batch endpoints are unchanged.

`media` is optional and backward-compatible: omitted or `enabled:false` keeps
the legacy package-only behavior. If media is explicitly enabled while no
worker is injected/configured, the run is blocked with capability
`media_worker`; worker capability blockers are passed through as run blockers,
while other worker errors fail the run.

## Recovery and fencing

The current worker is deliberately safe for a single active service process.
Each run has a renewable lease and every worker write carries a fencing token.
Startup requeues `running` rows only when no unexpired lease exists; a live
lease is left untouched. An expired worker cannot update the run, append an
event, or record an artifact after another worker acquires the next token.
Shutdown requeues non-terminal work unless the user explicitly cancelled it.

This is not a claim of full multi-instance orchestration: there is no external
heartbeat or process membership service, and only the SQLite lease clock is
used to detect liveness. Deployments must therefore run one active worker for
this vertical slice. A future multi-instance implementation must preserve the
same lease-aware recovery and compare-and-swap/fencing rules.
