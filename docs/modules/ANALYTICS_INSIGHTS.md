# Analytics insights

Analytics uses an independent SQLite database at
`VIDEO_FACTORY_DATA_DIR/analytics/analytics.sqlite`, or the path configured by
`ANALYTICS_DB_PATH`. The server opens it during startup and closes it during
graceful shutdown.

Scheduled polling is opt-in: `ANALYTICS_POLL_ENABLED` defaults to `false`.
When enabled, the worker runs on a `15s` tick, discovers reserved, non-draft
publications through a durable cursor. Pre-existing manual-sync `JobDone` jobs
are reactivated only for eligible nonterminal, non-expired publications;
terminal and expired publications are not reactivated. Each scheduled pass
performs a read-only `GetForAnalytics` from the local publication ledger; it
does not query external platform review state, publish, or change publication
state. Defaults are a `10m` review interval, a `1h` metrics interval, and a
`168h` metrics window. Blocked metrics capability is rechecked on a capped
schedule and can recover when the required configuration becomes available.

`POST /api/v1/publications/{id}/sync` remains the manual, bounded pass for the
requested publication. It is unchanged by the scheduled worker. Lease expiry
and fencing remain enforced for every snapshot and event write.

The analytics endpoints reuse the publication Bearer token and exact-origin
CORS policy:

- `GET /api/v1/publications/{id}/insights` returns durable review, metric, event,
  and sync-job state.
- `POST /api/v1/publications/{id}/sync` performs one manual pass.
- `GET /api/v1/analytics/capabilities` reports provider state without invoking
  platform operations.
- `GET /api/v1/analytics/summary` returns aggregate durable counts.

Douyin is read-only and requires its configured access token, open ID, and
metrics scope. Its provider uses the official `POST /video/data/?open_id=...`
shape with JSON `item_ids`, and accepts only an exact
`data.list[].item_id` match with its `statistics`. Scheduled metrics polling
runs only after the local ledger has verified the publication as `published`;
it does not perform platform publication verification or external review-state
queries. Xiaohongshu and Bilibili are explicitly unavailable for analytics;
their providers never make network requests or claim observations. No live
platform credentials or production validation are assumed, so live metrics
remain unverified platform observations. Duplicate observations are retained
once by their observation hash.
