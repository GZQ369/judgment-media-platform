# Production Baseline: Accepted 2026-10-12

## Status

This record closes the A+B implementation round. The task asset API canary
described below was rendered on 2026-10-02. The 2026-10-12 closeout validated
the implementation and regression tests; it did not rerender the canary.

## Implemented Scope

### A. RetryOnce child receipt

A durable child retry receipt is bound to parent ID, parent attempt, and child
ID. Replaying the same operation does not consume another child retry. The
implementation covers the SQLite child-store receipt and coordination with
Distribution and pipeline.

### B. Task asset snapshots and scene references

Task-scoped asset snapshots keep inputs stable, and per-scene asset references
identify intended assets. The relative-path P2 fix normalizes relative data
and output paths for the allowlisted asset server. The Windows-ID P2 fix
rejects reserved device names and case-insensitive duplicate IDs. Focused
regression coverage is in `internal/mediaworker/assets_test.go` and
`internal/distribution/media_assets_test.go`.

## Verification: 2026-10-12

- `go test ./...` passed after both P2 fixes in an execution environment that
  permitted local loopback. The ordinary sandbox denied socket access.
- `go vet ./...` exited 0.
- `go build -mod=readonly ./...` exited 0.
- `gofmt -l` produced no output; `git diff --check` exited 0.
- Sol's read-only review confirmed both P2 findings closed, with no remaining
  blockers.
- Earlier reported checks: A runstore/pipeline/RetryOnce tests passed; legacy
  three-product PowerShell smoke completed; Python bridge tests passed (2);
  invalid asset paths, an empty asset array, and disabled media with assets
  each returned HTTP 400.

## Original API Canary: 2026-10-02

- Run: `run_a76e5127-9213-43d8-b800-846a77f58966`.
- Data directory: `data/production-assets-20261002`.
- Path: API -> task asset snapshot -> OpenMontage -> `media_ready`.
- Video: 15.552 seconds, 1080x1920, with an audio track.
- Narration: 2x speed, 14.477868 seconds.
- Extracted frames confirmed three distinct routed assets: two PNG images and
  one MP4 video.
- Six ledger artifacts were registered: package, spec, input manifest, video,
  cover, and audio; production parameters were recorded.

This is a technical sample, not a publication-quality deliverable. It was not
rerendered on 2026-10-12: the path and ID fixes were verified by regression
tests and the full test suite. The sample has not been published. Piper model
licensing was not confirmed for this test. Cross-scene subtitle behavior,
semantic image selection, and expressive narration still need improvement.

## Acceptance and Remaining Work

Both A and B are accepted for this round as of 2026-10-12. The full
keyword -> research -> render -> publish workflow is not complete. Keyword
discovery, asset planning, and the end-to-end platform publication loop remain
future work; this canary does not establish publication or autonomous
production. Platform publication and analytics availability are not claimed.
