package distribution

import (
	"strings"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
)

// contentAtomOffering adapts a validated research/judgment atom to the
// existing deterministic workflow input. It deliberately uses a capability
// offering and leaves selling points empty: an editorial claim is not a
// product benefit. The workflow can therefore reuse its content, storyboard,
// media, and package stages without re-running research or judgment.
func contentAtomOffering(atom domain.ContentAtom) domain.OfferingInput {
	title := firstText(atom.Title, atom.Insight, atom.Claim)
	evidence := strings.Join(atom.EvidenceIDs, ", ")
	insight := firstText(atom.Insight, "基于已引用证据继续核验这个判断")
	boundary := firstText(atom.CounterPoint, "发布时同时说明证据范围和适用边界")
	cta := firstText(atom.CTA, atom.DesiredAction, "欢迎在评论区讨论你的场景")
	points := []string{
		"观点：" + firstText(atom.Claim, atom.Hook),
		"洞察：" + insight,
		"边界：" + boundary,
	}

	descriptionLines := []string{
		"内容钩子：" + atom.Hook,
		"核心观点：" + firstText(atom.Claim, atom.Hook),
		"延伸洞察：" + insight,
		"反方或限制：" + boundary,
		"证据引用：" + evidence,
	}
	return domain.OfferingInput{
		Kind:           domain.OfferingKindCapability,
		Name:           title,
		Title:          title,
		Description:    strings.Join(descriptionLines, "\n"),
		ValuePoints:    points,
		Problems:       []string{atom.Hook},
		TargetAudience: atom.Audience,
		ProofPoints:    append([]string(nil), atom.EvidenceIDs...),
		CallToAction:   cta,
		DeliveryMode:   "content_atom",
	}
}
