package distribution

import (
	"context"
	"github.com/GZQ369/judgment-media-platform/internal/mediaworker"
	"testing"
)

func TestProductionAcceptanceRequiresFreshVideoAndCover(t *testing.T) {
	manifest := &mediaworker.ArtifactManifest{Artifacts: []mediaworker.Artifact{{ID: "rendered-video", Kind: "video", Path: "missing.mp4"}}}
	if err := validateProductionRender(context.Background(), manifest, "", t.TempDir(), "ffprobe"); err == nil {
		t.Fatal("missing fresh video and cover must fail")
	}
}
