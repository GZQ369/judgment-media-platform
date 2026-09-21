# Persistent Research Pipeline Runs

`internal/pipeline` coordinates the durable boundary:

`ResearchTopic -> JudgmentWorkflow -> ContentAtom[] -> child DistributionRun[]`

The package owns its own SQLite database and tables (`pipeline_runs`,
`pipeline_events`, and `pipeline_leases`). It does not reuse the distribution
runstore tables. A `PipelineRequest` contains an idempotency key, a
`domain.ResearchTopic`, delivery targets, template, variant count, and optional
media. When a child is created, only the atom and one target are sent in a
`domain.DistributionRequest`; the ResearchTopic never crosses that boundary.

## State machine

```text
queued -> running -> distribution_ready
                    \-> blocked
                    \-> failed
                    \-> cancelled
```

`distribution_ready` is reached only when every child is `package_ready` or
`media_ready`. It means the distribution packages are ready for a later
publication step; it does not mean published. A blocked child makes the parent
`blocked` while retaining the complete child list and observed child blocker.
Other child failures make the parent `failed`.

## Idempotency and recovery

Each atom/target pair uses the stable key
`sha256(parent_pipeline_id + NUL + atom_id + NUL + platform + NUL + account_id + NUL + format)`
with the `pipeline-child-` prefix. Replaying a parent, retrying it, or
restarting the process therefore calls `StartRun` with the same child key and
cannot create a duplicate child in a conforming distribution service.
Each successful child creation or idempotent reuse is fenced into the parent
immediately before the next fan-out call. A later child error therefore leaves
all earlier child IDs and their last known states durable.

The pipeline worker takes a SQLite lease with a fencing token. Writes made by
an expired worker are rejected. Startup recovery and the periodic reaper turn
expired `running` leases back into `queued` work. The worker heartbeat renews
the lease while the judgment workflow or child readiness wait is executing.
Child readiness is checked with a cancellable timer and a configured maximum
wait; it never busy-polls.

## Capability blockers

Missing `JudgmentRunner` persists a structured blocker with capability
`judgment_workflow`. Missing `DistributionStarter` uses
`distribution_starter`. Capability errors from research, orchestration, or
judgment are translated to a pipeline `CapabilityBlock` and persist as
`blocked`, rather than being represented as a fabricated judgment or a
published result. Ordinary errors are `failed` and retain a retryable
`RunError`.

## Operations

`Service` provides `Start`, `Stop`, `StartRun`, `GetRun`, `ListRuns`, `Events`,
`Cancel`, and `Retry`. `Store` exposes the durable ledger operations for tests
and hosts that need direct recovery/lease control. A retry increments the
parent attempt but preserves child idempotency keys and the child relationship.
