package db

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/redis/rueidis"
)

// bulkBatch is how many keys a bulk action handles at a time.
const bulkBatch = 500

// EachMatching scans the keys matching a pattern, of every master, a batch
// at a time, until each returns an error or none are left.
func (k *KV) EachMatching(ctx context.Context, pattern string, each func(keys []string) error) error {
	var pos ScanPos
	for {
		keys, next, done, err := k.Scan(ctx, pos, pattern, "", bulkBatch)
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if err := each(keys); err != nil {
				return err
			}
		}
		if done {
			return nil
		}
		pos = next
	}
}

// CountMatching counts the keys matching a pattern, and names up to
// sample of them.
func (k *KV) CountMatching(ctx context.Context, pattern string, sample int) (int64, []string, error) {
	var n int64
	var names []string
	err := k.EachMatching(ctx, pattern, func(keys []string) error {
		n += int64(len(keys))
		if room := sample - len(names); room > 0 {
			names = append(names, keys[:min(room, len(keys))]...)
		}
		return nil
	})
	return n, names, err
}

// DeleteMatching unlinks the keys matching a pattern, each with a command
// of its own so that a cluster routes it to its slot, telling progress
// how many went. It returns how many were deleted.
func (k *KV) DeleteMatching(ctx context.Context, pattern string, progress func(int64)) (int64, error) {
	var n int64
	err := k.EachMatching(ctx, pattern, func(keys []string) error {
		cmds := make(rueidis.Commands, len(keys))
		for i, key := range keys {
			cmds[i] = k.Client.B().Unlink().Key(key).Build()
		}
		for _, res := range k.Client.DoMulti(ctx, cmds...) {
			gone, err := res.AsInt64()
			if err != nil {
				return err
			}
			n += gone
		}
		progress(n)
		return ctx.Err()
	})
	return n, err
}

// KeyDump is a key with its value and its time to live, as a line of an
// export: JSON, readable and written back by ImportKeys.
type KeyDump struct {
	Key  string `json:"key"`
	Type string `json:"type"`
	// TTL is the milliseconds the key had left to live; 0 for none.
	TTL int64 `json:"ttlMs,omitempty"`
	// Encoding is "base64" when the key's name and every text of its
	// value are base64 for bytes that are not UTF-8.
	Encoding string            `json:"encoding,omitempty"`
	String   *string           `json:"string,omitempty"`
	Items    []string          `json:"items,omitempty"`  // a list's or a set's
	Fields   [][2]string       `json:"fields,omitempty"` // a hash's
	Members  []ScoredMember    `json:"members,omitempty"`
	Entries  []StreamEntryDump `json:"entries,omitempty"`
}

// ScoredMember is a member of a sorted set with its score, as text:
// "inf" and "-inf" are scores JSON's numbers cannot hold.
type ScoredMember struct {
	Member string `json:"member"`
	Score  string `json:"score"`
}

// scoreText writes a score as ZADD reads it.
func scoreText(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "+inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// StreamEntryDump is an entry of a stream: its ID and its fields in order.
type StreamEntryDump struct {
	ID     string      `json:"id"`
	Fields [][2]string `json:"fields"`
}

// eachText calls f with every text of the dump: its key's name and its
// value's.
func (d *KeyDump) eachText(f func(*string)) {
	f(&d.Key)
	if d.String != nil {
		f(d.String)
	}
	for i := range d.Items {
		f(&d.Items[i])
	}
	for i := range d.Fields {
		f(&d.Fields[i][0])
		f(&d.Fields[i][1])
	}
	for i := range d.Members {
		f(&d.Members[i].Member)
	}
	for i := range d.Entries {
		for j := range d.Entries[i].Fields {
			f(&d.Entries[i].Fields[j][0])
			f(&d.Entries[i].Fields[j][1])
		}
	}
}

// DumpKey reads a key whole, as an export writes it; ok is false for a
// key gone, or of a type an export does not hold, as a module's.
func (k *KV) DumpKey(ctx context.Context, key string) (d KeyDump, ok bool, err error) {
	b := k.Client.B()
	res := k.Client.DoMulti(ctx, b.Type().Key(key).Build(), b.Pttl().Key(key).Build())
	typ, err := res[0].ToString()
	if err != nil {
		return d, false, err
	}
	d = KeyDump{Key: key, Type: typ}
	if ms, err := res[1].AsInt64(); err == nil && ms > 0 {
		d.TTL = ms
	}
	switch typ {
	case "string":
		s, err := k.Client.Do(ctx, b.Get().Key(key).Build()).ToString()
		if err != nil {
			return d, false, ignoreNil(err)
		}
		d.String = &s
	case "list":
		d.Items, err = k.Client.Do(ctx, b.Lrange().Key(key).Start(0).Stop(-1).Build()).AsStrSlice()
	case "set":
		d.Items, err = k.Client.Do(ctx, b.Smembers().Key(key).Build()).AsStrSlice()
	case "hash":
		var m []string
		m, err = k.Client.Do(ctx, b.Hgetall().Key(key).Build()).AsStrSlice()
		for i := 0; i+1 < len(m); i += 2 {
			d.Fields = append(d.Fields, [2]string{m[i], m[i+1]})
		}
	case "zset":
		var zs []rueidis.ZScore
		zs, err = k.Client.Do(ctx, b.Zrange().Key(key).Min("0").Max("-1").Withscores().Build()).AsZScores()
		for _, z := range zs {
			d.Members = append(d.Members, ScoredMember{Member: z.Member, Score: scoreText(z.Score)})
		}
	case "stream":
		// XRANGE gives an entry's fields as a map, losing their order.
		var raw []rueidis.RedisMessage
		raw, err = k.Client.Do(ctx, b.Xrange().Key(key).Start("-").End("+").Build()).ToArray()
		for _, m := range raw {
			entry, aerr := m.ToArray()
			if aerr != nil || len(entry) != 2 {
				return d, false, fmt.Errorf("an entry of %s: %v", key, aerr)
			}
			id, _ := entry[0].ToString()
			kv, _ := entry[1].AsStrSlice()
			e := StreamEntryDump{ID: id}
			for i := 0; i+1 < len(kv); i += 2 {
				e.Fields = append(e.Fields, [2]string{kv[i], kv[i+1]})
			}
			d.Entries = append(d.Entries, e)
		}
	default:
		return d, false, nil
	}
	if err != nil {
		return d, false, ignoreNil(err)
	}
	binary := false
	d.eachText(func(s *string) { binary = binary || !utf8.ValidString(*s) })
	if binary {
		d.Encoding = "base64"
		d.eachText(func(s *string) { *s = base64.StdEncoding.EncodeToString([]byte(*s)) })
	}
	return d, true, nil
}

// ignoreNil turns the error of a key gone between two commands into none.
func ignoreNil(err error) error {
	if rueidis.IsRedisNil(err) {
		return nil
	}
	return err
}

// ExportKeys writes the keys matching a pattern, one JSON line each, and
// returns how many were written, and how many of a type an export does
// not hold were passed over.
func (k *KV) ExportKeys(ctx context.Context, pattern string, w io.Writer, progress func(int64)) (written, skipped int64, err error) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	err = k.EachMatching(ctx, pattern, func(keys []string) error {
		for _, key := range keys {
			d, ok, err := k.DumpKey(ctx, key)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			if !ok {
				skipped++
				continue
			}
			if err := enc.Encode(d); err != nil {
				return err
			}
			written++
		}
		progress(written)
		return ctx.Err()
	})
	return written, skipped, err
}

// ReadKeyDumps reads the lines of an export.
func ReadKeyDumps(r io.Reader) ([]KeyDump, error) {
	var out []KeyDump
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<30)
	for line := 1; sc.Scan(); line++ {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var d KeyDump
		if err := json.Unmarshal(sc.Bytes(), &d); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if err := d.decode(); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		out = append(out, d)
	}
	return out, sc.Err()
}

// decode turns a dump's base64 texts back into their bytes, and checks it
// holds a value of its type.
func (d *KeyDump) decode() error {
	switch d.Encoding {
	case "":
	case "base64":
		var bad error
		d.eachText(func(s *string) {
			b, err := base64.StdEncoding.DecodeString(*s)
			if err != nil && bad == nil {
				bad = err
			}
			*s = string(b)
		})
		if bad != nil {
			return bad
		}
	default:
		return fmt.Errorf("the encoding %q is not known", d.Encoding)
	}
	if d.Key == "" {
		return errors.New("a key without a name")
	}
	empty := map[string]bool{"string": d.String == nil, "list": len(d.Items) == 0, "set": len(d.Items) == 0,
		"hash": len(d.Fields) == 0, "zset": len(d.Members) == 0, "stream": len(d.Entries) == 0}
	if e, known := empty[d.Type]; !known {
		return fmt.Errorf("%s is of the type %q, which is not imported", d.Key, d.Type)
	} else if e {
		return fmt.Errorf("%s has no value of its type %s", d.Key, d.Type)
	}
	// What the server could refuse once a key's commands are under way,
	// which a transaction does not undo, is refused before.
	for _, m := range d.Members {
		if _, err := strconv.ParseFloat(m.Score, 64); err != nil {
			return fmt.Errorf("%s: the score %q is not a number", d.Key, m.Score)
		}
	}
	var last [2]uint64
	for i, e := range d.Entries {
		id, ok := parseStreamID(e.ID)
		if !ok || i > 0 && (id[0] < last[0] || id[0] == last[0] && id[1] <= last[1]) || id == [2]uint64{} {
			return fmt.Errorf("%s: the entry ID %q is not one after the entry before it", d.Key, e.ID)
		}
		last = id
	}
	return nil
}

// parseStreamID reads a stream entry's ID, milliseconds-sequence.
func parseStreamID(s string) ([2]uint64, bool) {
	ms, seq, ok := strings.Cut(s, "-")
	a, err1 := strconv.ParseUint(ms, 10, 64)
	b, err2 := strconv.ParseUint(seq, 10, 64)
	return [2]uint64{a, b}, ok && err1 == nil && err2 == nil
}

// ImportCommands are the commands an import of the dumps runs, the key's
// deletion first when replace is set: as the safety policy weighs them.
func ImportCommands(d KeyDump, replace bool) [][]string {
	var out [][]string
	if replace {
		out = append(out, []string{"DEL", d.Key})
	}
	chunks := func(cmd string, args []string, per int) {
		for len(args) > 0 {
			n := min(len(args), per)
			out = append(out, append([]string{cmd, d.Key}, args[:n]...))
			args = args[n:]
		}
	}
	switch d.Type {
	case "string":
		out = append(out, []string{"SET", d.Key, *d.String})
	case "list":
		chunks("RPUSH", d.Items, bulkBatch)
	case "set":
		chunks("SADD", d.Items, bulkBatch)
	case "hash":
		var args []string
		for _, f := range d.Fields {
			args = append(args, f[0], f[1])
		}
		chunks("HSET", args, 2*bulkBatch)
	case "zset":
		var args []string
		for _, m := range d.Members {
			args = append(args, m.Score, m.Member)
		}
		chunks("ZADD", args, 2*bulkBatch)
	case "stream":
		for _, e := range d.Entries {
			cmd := []string{"XADD", d.Key, e.ID}
			for _, f := range e.Fields {
				cmd = append(cmd, f[0], f[1])
			}
			out = append(out, cmd)
		}
	}
	if d.TTL > 0 {
		out = append(out, []string{"PEXPIRE", d.Key, strconv.FormatInt(d.TTL, 10)})
	}
	return out
}

// ImportKeys writes keys as their dumps, checked as ReadKeyDumps checks
// them, hold them: each in one transaction, which a key there already
// passes over, or, when replace is set, deletes first. It returns how many
// were written and how many passed over.
func (k *KV) ImportKeys(ctx context.Context, dumps []KeyDump, replace bool, progress func(int64)) (written, skipped int64, err error) {
	for _, d := range dumps {
		wrote, err := k.importKey(ctx, d, replace)
		if err != nil {
			return written, skipped, fmt.Errorf("%s: %w", d.Key, err)
		}
		if wrote {
			written++
		} else {
			skipped++
		}
		progress(written)
		if err := ctx.Err(); err != nil {
			return written, skipped, err
		}
	}
	return written, skipped, nil
}

// importKey writes a key in MULTI and EXEC, on a connection of its own:
// all of it, or, when it fails or stops, none, the key replaced as it
// was. Without replace, WATCH passes over a key made meanwhile too.
func (k *KV) importKey(ctx context.Context, d KeyDump, replace bool) (wrote bool, err error) {
	cmds := ImportCommands(d, replace)
	err = k.Client.Dedicated(func(c rueidis.DedicatedClient) error {
		b := c.B()
		if !replace {
			if err := c.Do(ctx, b.Watch().Key(d.Key).Build()).Error(); err != nil {
				return err
			}
			n, err := c.Do(ctx, b.Exists().Key(d.Key).Build()).AsInt64()
			if err != nil || n > 0 {
				c.Do(ctx, b.Unwatch().Build())
				return err
			}
		}
		tx := make(rueidis.Commands, 0, len(cmds)+2)
		tx = append(tx, b.Multi().Build())
		for _, args := range cmds {
			tx = append(tx, b.Arbitrary(args[0]).Keys(args[1]).Args(args[2:]...).Build())
		}
		tx = append(tx, b.Exec().Build())
		res := c.DoMulti(ctx, tx...)
		// A command refused as it is queued aborts EXEC, which then
		// says so; the refusal says why.
		for i, r := range res[1 : len(res)-1] {
			if err := r.Error(); err != nil {
				return fmt.Errorf("%s: %w", cmds[i][0], err)
			}
		}
		exec := res[len(res)-1]
		if rueidis.IsRedisNil(exec.Error()) {
			return nil // made meanwhile, which WATCH saw
		}
		replies, err := exec.ToArray()
		if err != nil {
			return err
		}
		for i, r := range replies {
			if err := r.Error(); err != nil {
				return fmt.Errorf("%s: %w", cmds[i][0], err)
			}
		}
		wrote = true
		return nil
	})
	return wrote, err
}
