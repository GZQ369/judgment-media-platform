# Research Provider

## Scope

`internal/research` is the first source-grounded Research adapter. It is an
integrable component, not a claim that the product has a complete automated
research system in production. It does not modify `internal/domain`, the
distribution pipeline, API handlers, SQLite, or the existing workflow.

The package uses only the Go standard library. It can read an explicitly
provided HTTP(S) URL, retain the exact response bytes, and conservatively
extract text from RSS/Atom XML, JSON text fields, HTML, or plain text. RSS and
Atom entries are available through `SourceReader.FetchFeed` and
`ParseFeed`.

## Public API

- `NewSourceReader(FetchConfig)` creates a source reader.
- `(*SourceReader).Fetch(ctx, url)` returns a `SourceSnapshot` containing
  `SourceURL`, `FetchedAt`, SHA-256 `ContentHash`, `OriginalSize`, retained
  `Original` bytes, and conservative `Text`.
- `(*SourceReader).FetchFeed(ctx, url)` fetches and parses common RSS/Atom
  entries. `ParseFeed` can parse an already retained snapshot.
- `NewOpenAICompatibleClient(GatewayConfig)` creates a runtime-configured
  `TextGateway` client for `POST <baseURL>/chat/completions`.
- `Conduct` or `ConductWithConfig` asks that gateway for an `EvidenceBundle`.
- `NewRunner(SourceFetcher, TextGateway, RunnerConfig)` creates the injectable
  Researcher used by `internal/orchestration.JudgmentWorkflow`. `Runner`
  validates the `domain.ResearchTopic`, fetches `Topic.SourceURLs` with bounded
  concurrency, restores their input order, and invokes `ConductWithConfig`.
- `SourceError` preserves the failed URL and wraps the original fetch error.
  A failed source aborts the research call; the runner never returns a partial
  or fabricated `EvidenceBundle`.
- `EvidenceBundle.Validate` checks the minimum result schema and verifies that
  citations point to supplied snapshots.

`EvidenceBundle` owns the Research-side types: `SourceSnapshot`, `Evidence`,
`CounterEvidence` (represented as sourced `Evidence`), `HistoricalContext`,
`Contradiction`, and `Citation`. It does not depend on a new domain type. The
minimum accepted result has a topic and at least one evidence item. Every
non-empty observation needs a verifiable citation, and every contradiction
must cite both its supporting and opposing material. Judgment, thesis, and
ContentAtom drafts are intentionally absent from this contract; see
`docs/modules/JUDGMENT_ENGINE.md` for the next layer.

## Source safety

Source fetching accepts only absolute `http` or `https` URLs, rejects URL user
info, applies a timeout, a response byte limit, and a redirect limit, and
checks every redirect. Loopback, private, link-local, multicast, unspecified,
shared, documentation, and cloud-metadata address ranges are rejected. The
default source transport sets `Proxy: nil` and resolves/checks the address
again in `DialContext`, then dials the checked IP directly to reduce DNS
rebinding risk.

`FetchConfig.Client` is an explicitly supplied trusted transport boundary for
deterministic tests or callers that own the full network policy. Supplying a
custom transport can bypass the package's default dial policy and is therefore
not the default production path. Local gateway endpoints are independently
possible with `GatewayConfig.AllowLocal`; this does not and cannot enable
local source fetching.

The default source limit is 2 MiB. `ConductWithConfig` also applies a 4 MiB
combined source-text prompt budget so many individually valid snapshots cannot
create an unbounded gateway input. Both limits are configurable.

## Gateway contract

`GatewayConfig` carries `BaseURL`, `Model`, and `APIKey` at runtime only. This
package does not read secrets from environment variables or files. The API
key is sent only as a bearer header and is not included in errors.

The base URL must be an absolute HTTP(S) URL without query or fragment. The
client appends `chat/completions` with `url.JoinPath`, caps the response size,
applies a timeout, and rejects cross-origin redirects so an Authorization
header cannot be forwarded to another origin. A response without textual
`message.content` (including tool-only/refusal-only responses) returns the
explicit `ErrNoTextCapability`; it never fabricates a result.

## Evidence semantics

`Citation` requires the exact source URL, snapshot hash, and a quote that is
present in retained source bytes or extracted text. This proves provenance and
supports rechecking; it does **not** prove that the cited statement is
semantically true. Human or downstream domain validation remains responsible
for that judgment. An empty result, missing required sections, missing quotes,
unknown hashes, or uncited claims are rejected, so the adapter cannot produce
evidence without a source.

## Tests

The package tests use only `httptest` and in-memory snapshots. They cover RSS
fetch/retention, conservative text extraction, SSRF and unsafe redirects,
cancellation, source/gateway size limits, OpenAI-compatible request shape,
cross-origin gateway redirects, no-text gateway responses, input budget, and
uncited/empty result rejection. They do not fetch real content, use real API
keys, or perform real model generation.

`Runner` tests use an in-memory `SourceFetcher` and gateway. They cover bounded
source fan-out with stable input ordering, URL-audited source failure, missing
gateway capability, structured output validation, and context cancellation.
