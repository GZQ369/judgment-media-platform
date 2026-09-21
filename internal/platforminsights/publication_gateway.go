package platforminsights

import (
	"context"
	"encoding/json"

	publicationcore "github.com/GZQ369/judgment-media-platform/internal/publication"
)

// NewPublicationGateway adapts the durable publication orchestrator without
// exposing its concrete type through the analytics service contract.
func NewPublicationGateway(orchestrator *publicationcore.Orchestrator) PublicationGateway {
	return publicationGatewayAdapter{orchestrator: orchestrator}
}

// NewPublicationServiceGateway adapts the coordination service used by the
// HTTP server. It is read/reconcile-only and does not expose publish methods.
func NewPublicationServiceGateway(service *publicationcore.Service) PublicationGateway {
	return publicationServiceGatewayAdapter{service: service}
}

type publicationGatewayAdapter struct {
	orchestrator *publicationcore.Orchestrator
}

type publicationServiceGatewayAdapter struct {
	service *publicationcore.Service
}

func (a publicationServiceGatewayAdapter) Get(ctx context.Context, id string) (Publication, error) {
	if a.service == nil {
		return Publication{}, publicationcore.ErrNotFound
	}
	p, err := a.service.Get(ctx, id)
	if err != nil {
		return Publication{}, err
	}
	return neutralPublication(p), nil
}

func (a publicationServiceGatewayAdapter) Reconcile(ctx context.Context, id string) (Publication, error) {
	if a.service == nil {
		return Publication{}, publicationcore.ErrNotFound
	}
	p, err := a.service.Reconcile(ctx, publicationcore.ReconcileRequest{PublicationID: id})
	if err != nil {
		if p.ID == "" {
			return Publication{}, err
		}
		return neutralPublication(p), err
	}
	return neutralPublication(p), nil
}

func (a publicationGatewayAdapter) Get(ctx context.Context, id string) (Publication, error) {
	p, err := a.orchestrator.Get(ctx, id)
	if err != nil {
		return Publication{}, err
	}
	return neutralPublication(p), nil
}

func (a publicationGatewayAdapter) Reconcile(ctx context.Context, id string) (Publication, error) {
	p, err := a.orchestrator.Reconcile(ctx, publicationcore.ReconcileRequest{PublicationID: id})
	if err != nil {
		// Reconcile returns the durable snapshot alongside pending/unknown errors.
		if p.ID == "" {
			return Publication{}, err
		}
		neutral := neutralPublication(p)
		return neutral, err
	}
	return neutralPublication(p), nil
}

func neutralPublication(p publicationcore.Publication) Publication {
	result := Publication{
		ID: p.ID, Platform: string(p.Package.Platform), Account: p.Package.Account,
		ExternalID: p.ExternalID, ExternalURL: p.ExternalURL,
		Status: PublicationStatus(p.Status), UpdatedAt: p.UpdatedAt,
	}
	var evidence Evidence
	if json.Unmarshal([]byte(p.Evidence), &evidence) == nil {
		result.Evidence = evidence
	}
	return result
}
