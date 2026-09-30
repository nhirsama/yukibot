package kernel

import "context"

// Feature is the lifecycle contract every process component implements.
type Feature interface {
	Name() string
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}
