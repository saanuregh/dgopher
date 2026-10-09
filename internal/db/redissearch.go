package db

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/rueidis"
)

// HasSearch reports whether the server has Redis Search: Redis 8, or the
// module loaded.
func (k *KV) HasSearch() bool { return k.readOnly["FT._LIST"] || k.readOnly["FT.SEARCH"] }

// SearchIndexes lists the server's search indexes, by name.
func (k *KV) SearchIndexes(ctx context.Context) ([]string, error) {
	return k.Client.Do(ctx, k.Client.B().Arbitrary("FT._LIST").Build()).AsStrSlice()
}

// IndexField is a field a search index holds.
type IndexField struct {
	Name string // as the documents name it: a hash's field, or a JSON path
	As   string // as queries name it
	Type string // TEXT, TAG, NUMERIC, GEO, VECTOR, GEOSHAPE
}

// SearchIndex describes a search index.
type SearchIndex struct {
	On       string   // HASH or JSON
	Prefixes []string // the keys it indexes start so
	Fields   []IndexField
	Docs     int64
	// Properties are the rest FT.INFO tells, as its sizes, in its order.
	Properties []Property
}

// SearchIndexInfo describes a search index, as FT.INFO tells it.
func (k *KV) SearchIndexInfo(ctx context.Context, index string) (SearchIndex, error) {
	var out SearchIndex
	msg, err := k.Client.Do(ctx, k.Client.B().Arbitrary("FT.INFO").Args(index).Build()).ToMessage()
	if err != nil {
		return out, err
	}
	list, err := msg.ToArray()
	if err != nil {
		return out, err
	}
	for i := 0; i+1 < len(list); i += 2 {
		name, _ := list[i].ToString()
		value := list[i+1]
		switch name {
		case "index_definition":
			def := asMap(value)
			out.On = fieldString(def, "key_type")
			if p, ok := def["prefixes"]; ok {
				out.Prefixes, _ = p.AsStrSlice()
			}
		case "attributes":
			attrs, _ := value.ToArray()
			for _, a := range attrs {
				f := asMap(a)
				out.Fields = append(out.Fields, IndexField{Name: fieldString(f, "identifier"), As: fieldString(f, "attribute"), Type: fieldString(f, "type")})
			}
		case "num_docs":
			out.Docs, _ = value.AsInt64()
		case "index_name", "index_options":
		default:
			if s, err := value.ToString(); err == nil {
				out.Properties = append(out.Properties, Property{Name: name, Value: s})
			} else if n, err := value.AsInt64(); err == nil {
				out.Properties = append(out.Properties, Property{Name: name, Value: strconv.FormatInt(n, 10)})
			} else if f, err := value.AsFloat64(); err == nil {
				out.Properties = append(out.Properties, Property{Name: name, Value: strconv.FormatFloat(f, 'g', 4, 64)})
			}
		}
	}
	return out, nil
}

// IndexHit is a document a search found.
type IndexHit struct {
	Key    string
	Fields [][2]string // as the index returns them, a JSON document's as $
}

// Search runs a query on an index, as FT.SEARCH, and returns how many
// documents match, and the first n of them.
func (k *KV) Search(ctx context.Context, index, query string, n int64) (int64, []IndexHit, error) {
	if query == "" {
		query = "*"
	}
	raw, err := k.Client.Do(ctx, k.Client.B().Arbitrary("FT.SEARCH").Args(index, query, "LIMIT", "0", strconv.FormatInt(n, 10)).Build()).ToArray()
	if err != nil {
		return 0, nil, err
	}
	if len(raw) == 0 {
		return 0, nil, errors.New("FT.SEARCH gave nothing")
	}
	total, _ := raw[0].AsInt64()
	var hits []IndexHit
	for i := 1; i < len(raw); i++ {
		key, err := raw[i].ToString()
		if err != nil {
			return 0, nil, fmt.Errorf("FT.SEARCH gave %v where a key was", raw[i])
		}
		h := IndexHit{Key: key}
		// The fields follow, unless the query asked for none (NOCONTENT),
		// or nil, for a document deleted since it was indexed.
		if i+1 < len(raw) && raw[i+1].IsNil() {
			i++
		} else if i+1 < len(raw) {
			if fields, err := raw[i+1].AsStrSlice(); err == nil {
				for j := 0; j+1 < len(fields); j += 2 {
					h.Fields = append(h.Fields, [2]string{fields[j], fields[j+1]})
				}
				i++
			}
		}
		hits = append(hits, h)
	}
	return total, hits, nil
}

// Sample is a value of a time series, at its time.
type Sample struct {
	At    time.Time
	Value float64
}

// SeriesInfo describes a time series, as TS.INFO tells it: its first and
// last samples' times, and the rest, as its labels, as properties.
func (k *KV) SeriesInfo(ctx context.Context, key string) (first, last time.Time, props []Property, err error) {
	raw, err := k.Client.Do(ctx, k.Client.B().Arbitrary("TS.INFO").Keys(key).Build()).ToArray()
	if err != nil {
		return first, last, nil, err
	}
	samples := int64(-1)
	for i := 0; i+1 < len(raw); i += 2 {
		name, _ := raw[i].ToString()
		value := raw[i+1]
		switch name {
		case "totalSamples":
			samples, _ = value.AsInt64()
		case "firstTimestamp", "lastTimestamp":
			ms, _ := value.AsInt64()
			if name == "firstTimestamp" {
				first = time.UnixMilli(ms)
			} else {
				last = time.UnixMilli(ms)
			}
			continue
		case "labels", "rules":
			// A label is a name with its value; a rule, the series it
			// writes to, its bucket and its aggregation.
			items, _ := value.ToArray()
			var parts []string
			for _, item := range items {
				fields, _ := item.ToArray()
				words := make([]string, len(fields))
				for w, f := range fields {
					words[w] = messageText(f)
				}
				sep := " "
				if name == "labels" {
					sep = "="
				}
				parts = append(parts, strings.Join(words, sep))
			}
			if len(parts) > 0 {
				props = append(props, Property{Name: name, Value: strings.Join(parts, ", ")})
			}
			continue
		}
		if s, err := value.ToString(); err == nil {
			if s != "" {
				props = append(props, Property{Name: name, Value: s})
			}
		} else if n, err := value.AsInt64(); err == nil {
			props = append(props, Property{Name: name, Value: strconv.FormatInt(n, 10)})
		}
	}
	if samples == 0 {
		// Without samples, its times are 0, which is no sample's.
		first, last = time.Time{}, time.Time{}
	}
	return first, last, props, nil
}

// messageText is a reply's text, or its number written as text: a time
// series' rule holds both.
func messageText(m rueidis.RedisMessage) string {
	if s, err := m.ToString(); err == nil {
		return s
	}
	if n, err := m.AsInt64(); err == nil {
		return strconv.FormatInt(n, 10)
	}
	if f, err := m.AsFloat64(); err == nil {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	return ""
}

// SeriesRange reads a time series' samples from one time to another; with
// a bucket, the average of each bucket's.
func (k *KV) SeriesRange(ctx context.Context, key string, from, to time.Time, bucket time.Duration) ([]Sample, error) {
	args := []string{strconv.FormatInt(from.UnixMilli(), 10), strconv.FormatInt(to.UnixMilli(), 10)}
	if ms := bucket.Milliseconds(); ms > 0 {
		args = append(args, "AGGREGATION", "avg", strconv.FormatInt(ms, 10))
	}
	raw, err := k.Client.Do(ctx, k.Client.B().Arbitrary("TS.RANGE").Keys(key).Args(args...).Build()).ToArray()
	if err != nil {
		return nil, err
	}
	out := make([]Sample, 0, len(raw))
	for _, m := range raw {
		pair, err := m.ToArray()
		if err != nil || len(pair) != 2 {
			return nil, fmt.Errorf("TS.RANGE gave %v, not a time with its value", m)
		}
		ms, _ := pair[0].AsInt64()
		v, err := pair[1].AsFloat64()
		if err != nil {
			s, _ := pair[1].ToString()
			if v, err = strconv.ParseFloat(s, 64); err != nil {
				return nil, err
			}
		}
		out = append(out, Sample{At: time.UnixMilli(ms), Value: v})
	}
	return out, nil
}
