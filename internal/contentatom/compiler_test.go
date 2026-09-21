package contentatom

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/judgment"
	"github.com/GZQ369/judgment-media-platform/internal/research"
)

func TestCompilerDefaultsToOneValidatedAtomWithStableIDs(t *testing.T) {
	topic, bundle, result, thesis := compilerFixtures()
	compiler := NewCompiler(Options{})
	first, err := compiler.Compile(context.Background(), topic, bundle, result, thesis)
	if err != nil {
		t.Fatal(err)
	}
	second, err := compiler.Compile(context.Background(), topic, bundle, result, thesis)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || !reflect.DeepEqual(first, second) {
		t.Fatalf("compiled atoms are not deterministic: first=%#v second=%#v", first, second)
	}
	atom := first[0]
	if atom.SchemaVersion != domain.ContentAtomSchemaVersion || atom.Revision != 1 || atom.SourceType != "research" || atom.ThesisID == "" || len(atom.EvidenceIDs) != 1 {
		t.Fatalf("atom contract = %#v", atom)
	}
	if err := atom.Validate(); err != nil {
		t.Fatalf("compiled atom does not validate: %v", err)
	}
}

func TestCompilerPairwiseAndCartesianRespectLimits(t *testing.T) {
	topic, bundle, result, thesis := compilerFixtures()
	topic.Audiences = []string{"builders", "founders", "leaders"}
	topic.Angles = []string{"evidence", "cost"}
	topic.MaxAtoms = 4

	pairwise, err := NewCompiler(Options{MaxAtoms: 8, Strategy: StrategyPairwise}).Compile(context.Background(), topic, bundle, result, thesis)
	if err != nil {
		t.Fatal(err)
	}
	if len(pairwise) != 3 {
		t.Fatalf("pairwise count = %d, want 3", len(pairwise))
	}
	if pairwise[0].Audience != "builders" || pairwise[1].Angle != "cost" || pairwise[2].Audience != "leaders" {
		t.Fatalf("pairwise combinations = %#v", pairwise)
	}

	cartesian, err := NewCompiler(Options{MaxAtoms: 3, Strategy: StrategyCartesian}).Compile(context.Background(), topic, bundle, result, thesis)
	if err != nil {
		t.Fatal(err)
	}
	if len(cartesian) != 3 || cartesian[0].Audience != "builders" || cartesian[1].Angle != "cost" || cartesian[2].Audience != "founders" {
		t.Fatalf("cartesian combinations = %#v", cartesian)
	}
}

func TestCompilerRejectsUntraceableCitation(t *testing.T) {
	topic, bundle, result, thesis := compilerFixtures()
	result.Citations[0].Quote = "not in source"
	if _, err := NewCompiler(Options{}).Compile(context.Background(), topic, bundle, result, thesis); err == nil {
		t.Fatal("Compile() accepted an untraceable citation")
	}
}

func compilerFixtures() (domain.ResearchTopic, research.EvidenceBundle, judgment.Judgment, judgment.ContentThesis) {
	snapshot := research.NewSourceSnapshot("https://example.com/source", "text/plain", []byte("A sourced fact."), time.Unix(1700000000, 0).UTC())
	citation := research.Citation{SourceURL: snapshot.SourceURL, ContentHash: snapshot.ContentHash, Quote: "A sourced fact."}
	bundle := research.EvidenceBundle{
		Topic:    "a grounded topic",
		Sources:  []research.SourceSnapshot{snapshot},
		Evidence: []research.Evidence{{Statement: "The source contains a fact.", Citations: []research.Citation{citation}}},
	}
	return domain.ResearchTopic{
			Topic: "a grounded topic", SourceURLs: []string{snapshot.SourceURL}, DesiredAction: "inspect the source",
		}, bundle,
		judgment.Judgment{Statement: "The fact supports a narrow conclusion.", Citations: []research.Citation{citation}, Confidence: 0.8},
		judgment.ContentThesis{Statement: "Make the conclusion useful to builders.", Audience: "builders", Angle: "evidence first", DesiredAction: "inspect the source", CTA: "read the source", Citations: []research.Citation{citation}, Confidence: 0.9}
}
