package backup

import "context"

// Sink stores and retrieves backup snapshot payloads.
type Sink interface {
	Name() string
	Put(ctx context.Context, id string, payload []byte) (location string, err error)
	List(ctx context.Context) ([]Listed, error)
	Get(ctx context.Context, id string) ([]byte, error)
	Delete(ctx context.Context, id string) error
}

// Listed is one snapshot entry returned by Sink.List.
type Listed struct {
	SnapshotID string
	Location   string
}
