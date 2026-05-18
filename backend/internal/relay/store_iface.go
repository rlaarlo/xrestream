package relay

import (
	"context"

	"restream/backend/internal/store"
)

// ChannelStore is the minimal surface of the persistent store that
// relay.Manager needs. It is implemented by *store.Store (Postgres-backed)
// on the control plane and by an in-memory shim on remote nodes.
type ChannelStore interface {
	GetChannelBySlug(ctx context.Context, slug string) (store.Channel, error)
	ActiveWorkerChannels(ctx context.Context) ([]store.Channel, error)
	SetWorkerStatus(ctx context.Context, id, workerStatus string, lastError *string) error
	SetSourceStatus(ctx context.Context, id string, statusCode int, errMessage *string) error
	TouchRequest(ctx context.Context, id string)
	IncrementMetric(ctx context.Context, channelID, field string, amount int64)
}
