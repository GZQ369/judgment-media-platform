# Analytics insights

Analytics uses an independent SQLite database at
`VIDEO_FACTORY_DATA_DIR/analytics/analytics.sqlite`, or the path configured by
`ANALYTICS_DB_PATH`. The server opens it during startup and closes it during
graceful shutdown.

There is no background analytics timer. `POST /api/v1/publications/{id}/sync`
performs one bounded, manual reconciliation pass and claims only the requested
publication's durable job. Lease expiry and fencing remain enforced for every
snapshot and event write.

The analytics endpoints reuse the publication Bearer token and exact-origin
CORS policy:

- `GET /api/v1/publications/{id}/insights` returns durable review, metric, event,
  and sync-job state.
- `POST /api/v1/publications/{id}/sync` performs one manual pass.
- `GET /api/v1/analytics/capabilities` reports provider state without invoking
  platform operations.
- `GET /api/v1/analytics/summary` returns aggregate durable counts.

Douyin is read-only and requires its configured access token, open ID, and
metrics scope. Xiaohongshu and Bilibili are explicitly unavailable for
analytics; their providers never make network requests or claim observations.
Only verified published evidence can trigger metric collection, and duplicate
observations are retained once by their observation hash.
