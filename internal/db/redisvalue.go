package db

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/rueidis"
)

// Field is an item of a key: a list's (its index in Name), a hash's, a
// set's or a sorted set's (with its Score) member, or a stream's entry
// (its ID in Name, its fields in Value).
type Field struct {
	Name  string
	Value string
	Score float64
	// TTL is a hash field's time to live: -1 without one, 0 unknown.
	TTL time.Duration
}

// ItemsPos is how far a key's items were read; its zero value is their
// start.
type ItemsPos struct {
	offset int64  // of a list or a sorted set
	cursor uint64 // of a hash's or a set's scan
	lastID string // of a stream
}

// ReadItems reads about n items of a key from pos: a list's, a sorted
// set's or a stream's in their order, a hash's or a set's as the server
// scans them, which may repeat one. It returns where the next items start,
// and whether none are left.
func (k *KV) ReadItems(ctx context.Context, key, typ string, pos ItemsPos, n int64) ([]Field, ItemsPos, bool, error) {
	b := k.Client.B()
	switch typ {
	case "list":
		items, err := k.Client.Do(ctx, b.Lrange().Key(key).Start(pos.offset).Stop(pos.offset+n-1).Build()).AsStrSlice()
		if err != nil {
			return nil, pos, false, err
		}
		out := make([]Field, len(items))
		for i, v := range items {
			out[i] = Field{Name: strconv.FormatInt(pos.offset+int64(i), 10), Value: v}
		}
		pos.offset += int64(len(items))
		return out, pos, int64(len(items)) < n, nil
	case "zset":
		items, err := k.Client.Do(ctx, b.Zrange().Key(key).Min(strconv.FormatInt(pos.offset, 10)).Max(strconv.FormatInt(pos.offset+n-1, 10)).Withscores().Build()).AsZScores()
		if err != nil {
			return nil, pos, false, err
		}
		out := make([]Field, len(items))
		for i, z := range items {
			out[i] = Field{Value: z.Member, Score: z.Score}
		}
		pos.offset += int64(len(items))
		return out, pos, int64(len(items)) < n, nil
	case "set", "hash":
		var out []Field
		for {
			cmd := b.Sscan().Key(key).Cursor(pos.cursor).Count(n).Build()
			if typ == "hash" {
				cmd = b.Hscan().Key(key).Cursor(pos.cursor).Count(n).Build()
			}
			e, err := k.Client.Do(ctx, cmd).AsScanEntry()
			if err != nil {
				return out, pos, false, err
			}
			if typ == "hash" {
				for i := 0; i+1 < len(e.Elements); i += 2 {
					out = append(out, Field{Name: e.Elements[i], Value: e.Elements[i+1]})
				}
			} else {
				for _, v := range e.Elements {
					out = append(out, Field{Value: v})
				}
			}
			pos.cursor = e.Cursor
			if pos.cursor == 0 {
				return out, pos, true, nil
			}
			if int64(len(out)) >= n {
				return out, pos, false, nil
			}
		}
	case "stream":
		// From the last entry read, which is passed over: "(" for an
		// exclusive start needs Redis 6.2.
		start, count := "-", n
		if pos.lastID != "" {
			start, count = pos.lastID, n+1
		}
		msgs, err := k.Client.Do(ctx, b.Xrange().Key(key).Start(start).End("+").Count(count).Build()).AsXRange()
		if err != nil {
			return nil, pos, false, err
		}
		got := int64(len(msgs))
		if len(msgs) > 0 && msgs[0].ID == pos.lastID {
			msgs = msgs[1:]
		}
		out := make([]Field, len(msgs))
		for i, m := range msgs {
			names := make([]string, 0, len(m.FieldValues))
			for n := range m.FieldValues {
				names = append(names, n)
			}
			sort.Strings(names)
			var sb strings.Builder
			for j, n := range names {
				if j > 0 {
					sb.WriteString("  ")
				}
				fmt.Fprintf(&sb, "%s=%s", n, m.FieldValues[n])
			}
			out[i] = Field{Name: m.ID, Value: sb.String()}
		}
		if len(msgs) > 0 {
			pos.lastID = msgs[len(msgs)-1].ID
		}
		return out, pos, got < count, nil
	case "array":
		// From the index after the last read, to the array's end.
		end, err := k.Client.Do(ctx, b.Arbitrary("ARLEN").Keys(key).Build()).AsInt64()
		if err != nil || pos.offset >= end {
			return nil, pos, true, err
		}
		raw, err := k.Client.Do(ctx, b.Arbitrary("ARSCAN").Keys(key).Args(strconv.FormatInt(pos.offset, 10), strconv.FormatInt(end-1, 10), "LIMIT", strconv.FormatInt(n, 10)).Build()).ToArray()
		if err != nil {
			return nil, pos, false, err
		}
		// Each an index with its value.
		out := make([]Field, 0, len(raw))
		for _, pair := range raw {
			p, err := pair.ToArray()
			if err != nil || len(p) != 2 {
				return nil, pos, false, fmt.Errorf("ARSCAN gave %v, not an index with its value", pair)
			}
			index, _ := p[0].AsInt64()
			value, _ := p[1].ToString()
			out = append(out, Field{Name: strconv.FormatInt(index, 10), Value: value})
		}
		if len(out) == 0 {
			return nil, pos, true, nil
		}
		last, _ := strconv.ParseInt(out[len(out)-1].Name, 10, 64)
		pos.offset = last + 1
		return out, pos, int64(len(out)) < n || pos.offset >= end, nil
	case "vectorset":
		// In the elements' order, each with its attributes.
		start := "-"
		if pos.lastID != "" {
			start = "(" + pos.lastID
		}
		elems, err := k.Client.Do(ctx, b.Arbitrary("VRANGE").Keys(key).Args(start, "+", strconv.FormatInt(n, 10)).Build()).AsStrSlice()
		if err != nil || len(elems) == 0 {
			return nil, pos, true, err
		}
		cmds := make(rueidis.Commands, len(elems))
		for i, e := range elems {
			cmds[i] = b.Arbitrary("VGETATTR").Keys(key).Args(e).Build()
		}
		out := make([]Field, len(elems))
		for i, res := range k.Client.DoMulti(ctx, cmds...) {
			attrs, _ := res.ToString() // none is nil
			out[i] = Field{Name: elems[i], Value: attrs}
		}
		pos.lastID = elems[len(elems)-1]
		return out, pos, int64(len(elems)) < n, nil
	}
	return nil, pos, true, fmt.Errorf("the type %q has no viewer", typ)
}

// ReadString reads a string's first limit bytes, all of it for 0.
func (k *KV) ReadString(ctx context.Context, key string, limit int64) (string, error) {
	b := k.Client.B()
	if limit == 0 {
		return k.Client.Do(ctx, b.Get().Key(key).Build()).ToString()
	}
	return k.Client.Do(ctx, b.Getrange().Key(key).Start(0).End(limit-1).Build()).ToString()
}

// FieldExpiry reports whether the server keeps a time to live for each
// field of a hash, as Redis does from 7.4.
func (k *KV) FieldExpiry() bool { return k.readOnly["HPTTL"] }

// FieldTTLs reads the times to live of a hash's fields: -1 for a field
// without one, or gone.
func (k *KV) FieldTTLs(ctx context.Context, key string, fields []string) ([]time.Duration, error) {
	if len(fields) == 0 {
		return nil, nil
	}
	ms, err := k.Client.Do(ctx, k.Client.B().Hpttl().Key(key).Fields().Numfields(int64(len(fields))).Field(fields...).Build()).AsIntSlice()
	if err != nil {
		return nil, err
	}
	out := make([]time.Duration, len(fields))
	for i := range out {
		out[i] = -1
		if i < len(ms) && ms[i] >= 0 {
			out[i] = time.Duration(ms[i]) * time.Millisecond
		}
	}
	return out, nil
}
