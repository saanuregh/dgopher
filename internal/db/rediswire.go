package db

import (
	"context"
	"slices"
	"time"

	"github.com/redis/rueidis"
)

// wiredClient records the commands a Redis client sends. A value, so that
// two wrappers of one node's client compare equal, as the clients do.
type wiredClient struct {
	rueidis.Client
	who *sendsAs
}

// RecordAs sets the connection its sends are recorded as, from now on,
// for this client and its node and dedicated clients: a connection
// renamed, or given another ID or environment, without connecting again.
func (k *KV) RecordAs(cfg Config) { k.who.set(cfg) }

// recordRedis gives the hook a command the server answered. A nil reply
// is an answer, not a failure.
func recordRedis(who *sendsAs, args []string, took time.Duration, err error) {
	if rueidis.IsRedisNil(err) {
		err = nil
	}
	sent(who.config(), Sent{Redis: args, Took: took, Err: err})
}

// recordCmd records a command from args, its copied arguments, which are
// nil when no hook was set as it was sent.
func recordCmd(who *sendsAs, args []string, start time.Time, err error) {
	if args != nil {
		recordRedis(who, args, time.Since(start), err)
	}
}

// commandArgs copies a command's arguments, which rueidis recycles once
// the command is sent; nil when no hook is set.
func commandArgs(cmd interface{ Commands() []string }) []string {
	if onSend.Load() == nil {
		return nil
	}
	return slices.Clone(cmd.Commands())
}

// multiArgs is commandArgs for each of multi, whose commands commands reads.
func multiArgs[T any](multi []T, commands func(*T) []string) [][]string {
	if onSend.Load() == nil {
		return nil
	}
	out := make([][]string, len(multi))
	for i := range multi {
		out[i] = slices.Clone(commands(&multi[i]))
	}
	return out
}

func recordMulti(who *sendsAs, args [][]string, start time.Time, resps []rueidis.RedisResult) {
	took := time.Since(start)
	for i, a := range args {
		var err error
		if i < len(resps) {
			err = resps[i].Error()
		}
		recordRedis(who, a, took, err)
	}
}

func (c wiredClient) Do(ctx context.Context, cmd rueidis.Completed) rueidis.RedisResult {
	args, start := commandArgs(&cmd), time.Now()
	resp := c.Client.Do(ctx, cmd)
	recordCmd(c.who, args, start, resp.Error())
	return resp
}

func (c wiredClient) DoMulti(ctx context.Context, multi ...rueidis.Completed) []rueidis.RedisResult {
	args, start := multiArgs(multi, (*rueidis.Completed).Commands), time.Now()
	resps := c.Client.DoMulti(ctx, multi...)
	recordMulti(c.who, args, start, resps)
	return resps
}

func (c wiredClient) DoCache(ctx context.Context, cmd rueidis.Cacheable, ttl time.Duration) rueidis.RedisResult {
	args, start := commandArgs(&cmd), time.Now()
	resp := c.Client.DoCache(ctx, cmd, ttl)
	recordCmd(c.who, args, start, resp.Error())
	return resp
}

func (c wiredClient) DoMultiCache(ctx context.Context, multi ...rueidis.CacheableTTL) []rueidis.RedisResult {
	args, start := multiArgs(multi, func(c *rueidis.CacheableTTL) []string { return c.Cmd.Commands() }), time.Now()
	resps := c.Client.DoMultiCache(ctx, multi...)
	recordMulti(c.who, args, start, resps)
	return resps
}

func (c wiredClient) DoStream(ctx context.Context, cmd rueidis.Completed) rueidis.RedisResultStream {
	args, start := commandArgs(&cmd), time.Now()
	s := c.Client.DoStream(ctx, cmd)
	recordCmd(c.who, args, start, s.Error())
	return s
}

func (c wiredClient) DoMultiStream(ctx context.Context, multi ...rueidis.Completed) rueidis.MultiRedisResultStream {
	args, start := multiArgs(multi, (*rueidis.Completed).Commands), time.Now()
	s := c.Client.DoMultiStream(ctx, multi...)
	took := time.Since(start)
	for _, a := range args {
		recordRedis(c.who, a, took, s.Error())
	}
	return s
}

// Receive records the subscription as it starts: it lasts until it ends.
func (c wiredClient) Receive(ctx context.Context, subscribe rueidis.Completed, fn func(msg rueidis.PubSubMessage)) error {
	recordCmd(c.who, commandArgs(&subscribe), time.Now(), nil)
	return c.Client.Receive(ctx, subscribe, fn)
}

func (c wiredClient) Dedicated(fn func(rueidis.DedicatedClient) error) error {
	return c.Client.Dedicated(func(dc rueidis.DedicatedClient) error {
		return fn(wiredDedicated{dc, c.who})
	})
}

func (c wiredClient) Dedicate() (rueidis.DedicatedClient, func()) {
	dc, cancel := c.Client.Dedicate()
	return wiredDedicated{dc, c.who}, cancel
}

func (c wiredClient) Nodes() map[string]rueidis.Client {
	nodes := c.Client.Nodes()
	out := make(map[string]rueidis.Client, len(nodes))
	for addr, n := range nodes {
		out[addr] = wiredClient{n, c.who}
	}
	return out
}

// wiredDedicated records the commands a dedicated connection sends.
type wiredDedicated struct {
	rueidis.DedicatedClient
	who *sendsAs
}

func (c wiredDedicated) Do(ctx context.Context, cmd rueidis.Completed) rueidis.RedisResult {
	args, start := commandArgs(&cmd), time.Now()
	resp := c.DedicatedClient.Do(ctx, cmd)
	recordCmd(c.who, args, start, resp.Error())
	return resp
}

func (c wiredDedicated) DoMulti(ctx context.Context, multi ...rueidis.Completed) []rueidis.RedisResult {
	args, start := multiArgs(multi, (*rueidis.Completed).Commands), time.Now()
	resps := c.DedicatedClient.DoMulti(ctx, multi...)
	recordMulti(c.who, args, start, resps)
	return resps
}

func (c wiredDedicated) Receive(ctx context.Context, subscribe rueidis.Completed, fn func(msg rueidis.PubSubMessage)) error {
	recordCmd(c.who, commandArgs(&subscribe), time.Now(), nil)
	return c.DedicatedClient.Receive(ctx, subscribe, fn)
}
