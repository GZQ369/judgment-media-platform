# Judgment Engine

`internal/research` emits an `research.EvidenceBundle`. It owns source
retrieval, snapshots, evidence, counter-evidence, historical context, and
contradictions. It does not decide what the evidence means and does not
produce content atoms or platform instructions.

`internal/judgment` consumes that bundle and exposes one boundary:

```go
judgment, thesis, err := judgment.NewService(model).Evaluate(ctx, bundle)
```

The service returns a sourced `Judgment` and `ContentThesis`. It does not
import workflow, media, publisher, or platform packages. A missing model
executor returns a `*judgment.CapabilityBlocker` with capability
`judgment_model_generation`; it never invents a result.

`internal/contentatom` is the explicit compiler boundary after Judgment. It
consumes `research.EvidenceBundle`, `judgment.Judgment`, and
`judgment.ContentThesis`, verifies every citation against the fetched source
set, and emits one or a bounded number of validated `domain.ContentAtom`
values. The default is one atom; additional audience/angle fan-out requires
an explicit limit and uses either pairwise or bounded Cartesian selection.
Atom and evidence IDs are deterministic hashes of the sourced inputs, so the
compiler does not invent provenance.

`internal/orchestration.JudgmentWorkflow` coordinates the three boundaries in
order: Researcher -> Judge -> Compiler. It emits structured lifecycle events
and returns a `CapabilityBlocker` when a dependency is not configured. This
workflow stops at ContentAtom: it does not call distribution, media, platform,
or publisher packages. The resulting atoms are ready for a later outer
Distribution facade, but this module does not connect that facade itself.
