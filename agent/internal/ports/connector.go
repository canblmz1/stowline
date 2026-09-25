package ports

import (
	"context"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

// BoundRepository is what the engine needs after the connector validates a descriptor.
type BoundRepository struct {
	Location     string
	Options      []KV
	Env          []EnvVar
	Capabilities domain.Capabilities
	BinaryPins   []domain.BinaryPin
}

type KV struct {
	Key   string
	Value string
}

// Connector understands backend/transport details. Domain code must not.
type Connector interface {
	Bind(ctx context.Context, d domain.RepositoryDescriptor) (BoundRepository, error)
}
