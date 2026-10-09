package db

import (
	"context"
	"time"
)

// StreamGroup is a consumer group of a stream.
type StreamGroup struct {
	Name          string
	Consumers     int64
	Pending       int64 // entries delivered and not acknowledged
	LastDelivered string
	// Lag is how many entries are left to deliver, -1 when the server
	// cannot tell (or is older than Redis 7).
	Lag int64
}

// StreamConsumer is a consumer of a group.
type StreamConsumer struct {
	Name    string
	Pending int64
	Idle    time.Duration // since it last read
}

// PendingEntry is an entry delivered to a consumer and not acknowledged.
type PendingEntry struct {
	ID         string
	Consumer   string
	Idle       time.Duration // since it was delivered
	Deliveries int64
}

// StreamGroups lists the consumer groups of a stream.
func (k *KV) StreamGroups(ctx context.Context, key string) ([]StreamGroup, error) {
	raw, err := k.Client.Do(ctx, k.Client.B().XinfoGroups().Key(key).Build()).ToArray()
	if err != nil {
		return nil, err
	}
	out := make([]StreamGroup, 0, len(raw))
	for _, m := range raw {
		f := asMap(m)
		g := StreamGroup{Name: fieldString(f, "name"), Consumers: fieldInt(f, "consumers", 0), Pending: fieldInt(f, "pending", 0),
			LastDelivered: fieldString(f, "last-delivered-id"), Lag: fieldInt(f, "lag", -1)}
		out = append(out, g)
	}
	return out, nil
}

// StreamConsumers lists the consumers of a group.
func (k *KV) StreamConsumers(ctx context.Context, key, group string) ([]StreamConsumer, error) {
	raw, err := k.Client.Do(ctx, k.Client.B().XinfoConsumers().Key(key).Group(group).Build()).ToArray()
	if err != nil {
		return nil, err
	}
	out := make([]StreamConsumer, 0, len(raw))
	for _, m := range raw {
		f := asMap(m)
		out = append(out, StreamConsumer{Name: fieldString(f, "name"), Pending: fieldInt(f, "pending", 0),
			Idle: time.Duration(fieldInt(f, "idle", 0)) * time.Millisecond})
	}
	return out, nil
}

// PendingEntries lists up to n entries delivered to a group's consumers
// and not acknowledged, oldest first.
func (k *KV) PendingEntries(ctx context.Context, key, group string, n int64) ([]PendingEntry, error) {
	raw, err := k.Client.Do(ctx, k.Client.B().Xpending().Key(key).Group(group).Start("-").End("+").Count(n).Build()).ToArray()
	if err != nil {
		return nil, err
	}
	out := make([]PendingEntry, 0, len(raw))
	for _, m := range raw {
		f, err := m.ToArray()
		if err != nil || len(f) < 4 {
			continue
		}
		var p PendingEntry
		p.ID, _ = f[0].ToString()
		p.Consumer, _ = f[1].ToString()
		ms, _ := f[2].AsInt64()
		p.Idle = time.Duration(ms) * time.Millisecond
		p.Deliveries, _ = f[3].AsInt64()
		out = append(out, p)
	}
	return out, nil
}
