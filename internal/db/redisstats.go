package db

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/redis/rueidis"
)

// SlowEntry is a command the server logged as slow.
type SlowEntry struct {
	Node     string // the server's address, on a cluster
	ID       int64
	At       time.Time
	Duration time.Duration
	// Args are the command and its arguments, as the server keeps them:
	// long ones cut, and those holding secrets left out.
	Args   []string
	Client string // its address, and its name after a space
}

// SlowLog reads up to n of the latest slow commands of each master,
// slowest first.
func (k *KV) SlowLog(ctx context.Context, n int64) ([]SlowEntry, error) {
	masters, err := k.mastersFor(ctx, false)
	if err != nil {
		return nil, err
	}
	var out []SlowEntry
	for _, c := range masters {
		node := ""
		if len(masters) > 1 {
			for addr, nc := range k.Client.Nodes() {
				if nc == c {
					node = addr
				}
			}
		}
		raw, err := c.Do(ctx, c.B().SlowlogGet().Count(n).Build()).ToArray()
		if err != nil {
			return nil, err
		}
		for _, m := range raw {
			fields, err := m.ToArray()
			if err != nil || len(fields) < 4 {
				return nil, fmt.Errorf("a slow log entry the app cannot read: %v", err)
			}
			e := SlowEntry{Node: node}
			e.ID, _ = fields[0].AsInt64()
			at, _ := fields[1].AsInt64()
			e.At = time.Unix(at, 0)
			us, _ := fields[2].AsInt64()
			e.Duration = time.Duration(us) * time.Microsecond
			e.Args, _ = fields[3].AsStrSlice()
			if len(fields) >= 6 {
				addr, _ := fields[4].ToString()
				name, _ := fields[5].ToString()
				e.Client = strings.TrimSpace(addr + " " + name)
			}
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Duration > out[j].Duration })
	return out, nil
}

// ResetSlowLog empties the slow log of every master.
func (k *KV) ResetSlowLog(ctx context.Context) error {
	masters, err := k.mastersFor(ctx, false)
	if err != nil {
		return err
	}
	for _, c := range masters {
		if err := c.Do(ctx, c.B().SlowlogReset().Build()).Error(); err != nil {
			return err
		}
	}
	return nil
}

// maxNamespaces is how many namespaces an analysis tells apart; those
// past it, as of keys named by a UUID first, are summed as one.
const maxNamespaces = 10_000

// OtherNamespaces is the prefix of the namespaces past maxNamespaces.
const OtherNamespaces = "\x00other"

// KeyStat is what an analysis found of a key.
type KeyStat struct {
	Key    string
	Type   string
	Memory int64         // bytes, as MEMORY USAGE estimates them
	TTL    time.Duration // -1 without one
}

// KeySum counts keys and the memory they take.
type KeySum struct {
	Keys   int64
	Memory int64
}

func (s *KeySum) add(memory int64) { s.Keys, s.Memory = s.Keys+1, s.Memory+memory }

// NamespaceStat sums the keys of a namespace: those whose names start
// with the same first part, before the separator.
type NamespaceStat struct {
	Prefix string // "" for keys without the separator
	KeySum
}

// MemoryReport is what an analysis of the keys found.
type MemoryReport struct {
	Keys     int64 // analysed
	Total    int64 // the server's keys, as DBSIZE counts them
	Memory   int64 // of the keys analysed
	Expiring int64 // of the keys analysed, those with a TTL
	// ExpiringWithin counts the expiring keys by when they expire: within
	// an hour, a day, a week, or later.
	ExpiringWithin [4]int64
	Largest        []KeyStat // the largest keys, largest first
	Namespaces     []NamespaceStat
	ByType         map[string]KeySum
}

// expiryBounds are the bounds of MemoryReport.ExpiringWithin.
var expiryBounds = []time.Duration{time.Hour, 24 * time.Hour, 7 * 24 * time.Hour}

// AnalyseMemory reads the type, size and TTL of up to limit keys, in
// SCAN's order, and sums them: the largest keys, the namespaces before
// sep, and how soon keys expire. progress tells the keys read.
func (k *KV) AnalyseMemory(ctx context.Context, sep string, limit int64, largest int, progress func(int64)) (*MemoryReport, error) {
	r := &MemoryReport{ByType: map[string]KeySum{}}
	spaces := map[string]*NamespaceStat{}
	var stats []KeyStat
	errDone := errors.New("enough keys")
	err := k.EachMatching(ctx, "*", func(keys []string) error {
		keys = keys[:min(int64(len(keys)), limit-r.Keys)]
		b := k.Client.B()
		cmds := make(rueidis.Commands, 0, 3*len(keys))
		for _, key := range keys {
			// The server's own sampling: SAMPLES 0 would walk each element
			// of a large key while the server waits.
			cmds = append(cmds, b.Type().Key(key).Build(), b.MemoryUsage().Key(key).Build(), b.Pttl().Key(key).Build())
		}
		res := k.Client.DoMulti(ctx, cmds...)
		for i, key := range keys {
			typ, err := res[3*i].ToString()
			if err != nil {
				return err
			}
			if typ == "none" {
				continue // gone since the scan
			}
			mem, err := res[3*i+1].AsInt64()
			if err != nil && !rueidis.IsRedisNil(err) {
				return fmt.Errorf("MEMORY USAGE: %w", err)
			}
			ms, _ := res[3*i+2].AsInt64()
			s := KeyStat{Key: key, Type: typ, Memory: mem, TTL: -1}
			if ms >= 0 {
				s.TTL = time.Duration(ms) * time.Millisecond
				r.Expiring++
				bucket := sort.Search(len(expiryBounds), func(j int) bool { return s.TTL <= expiryBounds[j] })
				r.ExpiringWithin[bucket]++
			}
			r.Keys++
			r.Memory += mem
			t := r.ByType[typ]
			t.add(mem)
			r.ByType[typ] = t
			prefix := ""
			if i := strings.Index(key, sep); i >= 0 && sep != "" {
				prefix = key[:i]
			}
			ns := spaces[prefix]
			if ns == nil && len(spaces) >= maxNamespaces {
				prefix = OtherNamespaces
				ns = spaces[prefix]
			}
			if ns == nil {
				ns = &NamespaceStat{Prefix: prefix}
				spaces[prefix] = ns
			}
			ns.add(mem)
			stats = append(stats, s)
		}
		// Keep the largest only, as more are read.
		if len(stats) > 4*largest {
			stats = topKeys(stats, largest)
		}
		progress(r.Keys)
		if r.Keys >= limit {
			return errDone
		}
		return ctx.Err()
	})
	if err != nil && err != errDone {
		return nil, err
	}
	r.Largest = topKeys(stats, largest)
	for _, ns := range spaces {
		r.Namespaces = append(r.Namespaces, *ns)
	}
	sort.Slice(r.Namespaces, func(i, j int) bool {
		if r.Namespaces[i].Memory != r.Namespaces[j].Memory {
			return r.Namespaces[i].Memory > r.Namespaces[j].Memory
		}
		return r.Namespaces[i].Prefix < r.Namespaces[j].Prefix
	})
	r.Total, err = k.DBSize(ctx)
	return r, err
}

// topKeys keeps the n largest keys, largest first.
func topKeys(stats []KeyStat, n int) []KeyStat {
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Memory != stats[j].Memory {
			return stats[i].Memory > stats[j].Memory
		}
		return stats[i].Key < stats[j].Key
	})
	return stats[:min(n, len(stats))]
}
