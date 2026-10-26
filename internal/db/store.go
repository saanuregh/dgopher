package db

import (
	"context"
	"fmt"
)

// Store is an open connection of any engine family: what a connection's
// life needs of it, whatever the engine.
type Store interface {
	Close() error
	// RouteDone closes when the SSH tunnel under the store ends, nil when
	// there is none; RouteErr then says why.
	RouteDone() <-chan struct{}
	RouteErr() error
	ServerVersion(ctx context.Context) string
	TLSState() string
	RecordAs(cfg Config)
}

var (
	_ Store = (*DB)(nil)
	_ Store = (*KV)(nil)
)

// OpenStore opens cfg with its engine family's client. On an error the
// store is nil, never a nil pointer inside the interface.
func OpenStore(ctx context.Context, cfg Config, knownHosts []string) (Store, error) {
	switch cfg.Engine.Kind() {
	case KindSQL:
		d, err := Open(ctx, cfg, knownHosts)
		if err != nil {
			return nil, err
		}
		return d, nil
	case KindKeyValue:
		kv, err := OpenRedis(ctx, cfg, knownHosts)
		if err != nil {
			return nil, err
		}
		return kv, nil
	}
	return nil, fmt.Errorf("no store opens %s", cfg.Engine.Label())
}
