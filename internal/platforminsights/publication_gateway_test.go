package platforminsights

import "testing"

func TestPublicationGatewayAdaptersExposeOptionalDiscovery(t *testing.T) {
	var _ PublicationGateway = publicationGatewayAdapter{}
	var _ PublicationGateway = publicationServiceGatewayAdapter{}
	var _ PublicationDiscovery = publicationGatewayAdapter{}
	var _ PublicationDiscovery = publicationServiceGatewayAdapter{}
	var _ PublicationSnapshotReader = publicationGatewayAdapter{}
	var _ PublicationSnapshotReader = publicationServiceGatewayAdapter{}
}
