package db

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/redis/rueidis"
)

// PubSubMessage is a message published to a channel a subscription
// listens to.
type PubSubMessage struct {
	At      time.Time
	Channel string
	Pattern string // the pattern it matched, for a pattern's subscription
	Message string
}

// IsPattern reports whether a channel name is a pattern, as PSUBSCRIBE
// takes: it holds a glob's characters.
func IsPattern(name string) bool { return strings.ContainsAny(name, "*?[") }

// Subscribe listens to channels, and to patterns of channels, on
// connections of their own, telling msg each message, until ctx ends or a
// server fails.
func (k *KV) Subscribe(ctx context.Context, channels, patterns []string, msg func(PubSubMessage)) error {
	if len(channels) == 0 && len(patterns) == 0 {
		return errors.New("name a channel or a pattern to listen to")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	b := k.Client.B()
	var cmds []rueidis.Completed
	if len(channels) > 0 {
		cmds = append(cmds, b.Subscribe().Channel(channels...).Build())
	}
	if len(patterns) > 0 {
		cmds = append(cmds, b.Psubscribe().Pattern(patterns...).Build())
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(cmds))
	for _, cmd := range cmds {
		wg.Go(func() {
			err := k.Client.Receive(ctx, cmd, func(m rueidis.PubSubMessage) {
				msg(PubSubMessage{At: time.Now(), Channel: m.Channel, Pattern: m.Pattern, Message: m.Message})
			})
			if err != nil && ctx.Err() == nil {
				errs <- err
				cancel()
			}
		})
	}
	wg.Wait()
	select {
	case err := <-errs:
		return err
	default:
		return nil
	}
}

// ChannelInfo is a channel some client listens to.
type ChannelInfo struct {
	Name        string
	Subscribers int64
}

// Channels lists the channels matching a pattern that clients listen to,
// with how many listen to each, most first. Each node of a cluster
// counts its own clients only: all are asked.
func (k *KV) Channels(ctx context.Context, pattern string) ([]ChannelInfo, error) {
	if pattern == "" {
		pattern = "*"
	}
	nodes := []rueidis.Client{k.Client}
	if k.Client.Mode() == rueidis.ClientModeCluster {
		nodes = nodes[:0]
		for _, c := range k.Client.Nodes() {
			nodes = append(nodes, c)
		}
	}
	listeners := map[string]int64{}
	for _, c := range nodes {
		b := c.B()
		names, err := c.Do(ctx, b.PubsubChannels().Pattern(pattern).Build()).AsStrSlice()
		if err != nil {
			return nil, err
		}
		if len(names) == 0 {
			continue
		}
		counts, err := c.Do(ctx, b.PubsubNumsub().Channel(names...).Build()).ToArray()
		if err != nil {
			return nil, err
		}
		for i := 0; i+1 < len(counts); i += 2 {
			name, _ := counts[i].ToString()
			n, _ := counts[i+1].AsInt64()
			listeners[name] += n
		}
	}
	out := make([]ChannelInfo, 0, len(listeners))
	for name, n := range listeners {
		out = append(out, ChannelInfo{Name: name, Subscribers: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Subscribers != out[j].Subscribers {
			return out[i].Subscribers > out[j].Subscribers
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}
