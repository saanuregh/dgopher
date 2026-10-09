package db

import (
	"context"
	"strconv"
)

// JSONDocument reads a RedisJSON key's document, as JSON text.
func (k *KV) JSONDocument(ctx context.Context, key string) (string, error) {
	return k.Client.Do(ctx, k.Client.B().Arbitrary("JSON.GET").Keys(key).Build()).ToString()
}

// Property is a name with its value, as a server describes a key.
type Property struct {
	Name  string
	Value string
}

// VectorSetInfo describes a vector set, as VINFO tells it.
func (k *KV) VectorSetInfo(ctx context.Context, key string) ([]Property, error) {
	raw, err := k.Client.Do(ctx, k.Client.B().Arbitrary("VINFO").Keys(key).Build()).ToArray()
	if err != nil {
		return nil, err
	}
	var out []Property
	for i := 0; i+1 < len(raw); i += 2 {
		name, _ := raw[i].ToString()
		out = append(out, Property{Name: name, Value: messageText(raw[i+1])})
	}
	return out, nil
}

// VectorOf reads an element's vector, as the set keeps it (quantized).
func (k *KV) VectorOf(ctx context.Context, key, element string) ([]float64, error) {
	raw, err := k.Client.Do(ctx, k.Client.B().Arbitrary("VEMB").Keys(key).Args(element).Build()).AsStrSlice()
	if err != nil {
		return nil, err
	}
	out := make([]float64, len(raw))
	for i, s := range raw {
		if out[i], err = strconv.ParseFloat(s, 64); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Similar finds up to n elements of a vector set most like an element,
// with their similarity, from 0 to 1, most like it first.
func (k *KV) Similar(ctx context.Context, key, element string, n int64) ([]Field, error) {
	raw, err := k.Client.Do(ctx, k.Client.B().Arbitrary("VSIM").Keys(key).Args("ELE", element, "WITHSCORES", "COUNT", strconv.FormatInt(n, 10)).Build()).AsStrSlice()
	if err != nil {
		return nil, err
	}
	var out []Field
	for i := 0; i+1 < len(raw); i += 2 {
		score, _ := strconv.ParseFloat(raw[i+1], 64)
		out = append(out, Field{Name: raw[i], Score: score})
	}
	return out, nil
}
