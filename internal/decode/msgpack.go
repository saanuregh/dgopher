package decode

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"time"
)

// maxDepth is how deep values may nest, past which a value is refused:
// a crafted one would exhaust the stack.
const maxDepth = 1000

// reader reads a value's bytes, failing past their end.
type reader struct {
	b   []byte
	pos int
}

func (r *reader) take(n int) ([]byte, error) {
	if n < 0 || r.pos+n > len(r.b) {
		return nil, errTruncated
	}
	out := r.b[r.pos : r.pos+n]
	r.pos += n
	return out, nil
}

func (r *reader) byte() (byte, error) {
	b, err := r.take(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

// uint reads an unsigned big-endian number of n bytes.
func (r *reader) uint(n int) (uint64, error) {
	b, err := r.take(n)
	if err != nil {
		return 0, err
	}
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v, nil
}

// count reads a length of n bytes, no more than the bytes left, each item
// taking one at least.
func (r *reader) count(n int) (int, error) {
	v, err := r.uint(n)
	if err != nil {
		return 0, err
	}
	if v > uint64(len(r.b)-r.pos) {
		return 0, errTruncated
	}
	return int(v), nil
}

// done fails when bytes follow the value.
func (r *reader) done() error {
	if r.pos != len(r.b) {
		return fmt.Errorf("%d bytes follow the value", len(r.b)-r.pos)
	}
	return nil
}

func decodeMsgpack(b []byte) (any, error) {
	r := &reader{b: b}
	v, err := r.msgpack(0)
	if err != nil {
		return nil, err
	}
	return v, r.done()
}

func (r *reader) msgpack(depth int) (any, error) {
	if depth > maxDepth {
		return nil, errors.New("the value nests too deep")
	}
	t, err := r.byte()
	if err != nil {
		return nil, err
	}
	switch {
	case t <= 0x7f:
		return int64(t), nil
	case t >= 0xe0:
		return int64(int8(t)), nil
	case t >= 0x80 && t <= 0x8f:
		return r.msgpackMap(int(t&0x0f), depth)
	case t >= 0x90 && t <= 0x9f:
		return r.msgpackArray(int(t&0x0f), depth)
	case t >= 0xa0 && t <= 0xbf:
		s, err := r.take(int(t & 0x1f))
		return string(s), err
	}
	switch t {
	case 0xc0:
		return nil, nil
	case 0xc2:
		return false, nil
	case 0xc3:
		return true, nil
	case 0xc4, 0xc5, 0xc6:
		n, err := r.count(1 << (t - 0xc4))
		if err != nil {
			return nil, err
		}
		b, err := r.take(n)
		return bytesValue(b), err
	case 0xc7, 0xc8, 0xc9:
		n, err := r.count(1 << (t - 0xc7))
		if err != nil {
			return nil, err
		}
		return r.msgpackExt(n)
	case 0xca:
		v, err := r.uint(4)
		return float64(math.Float32frombits(uint32(v))), err
	case 0xcb:
		v, err := r.uint(8)
		return math.Float64frombits(v), err
	case 0xcc, 0xcd, 0xce, 0xcf:
		return r.uint(1 << (t - 0xcc))
	case 0xd0, 0xd1, 0xd2, 0xd3:
		n := 1 << (t - 0xd0)
		v, err := r.uint(n)
		shift := 64 - 8*n
		return int64(v<<shift) >> shift, err
	case 0xd4, 0xd5, 0xd6, 0xd7, 0xd8:
		return r.msgpackExt(1 << (t - 0xd4))
	case 0xd9, 0xda, 0xdb:
		n, err := r.count(1 << (t - 0xd9))
		if err != nil {
			return nil, err
		}
		s, err := r.take(n)
		return string(s), err
	case 0xdc, 0xdd:
		n, err := r.count(2 << (t - 0xdc))
		if err != nil {
			return nil, err
		}
		return r.msgpackArray(n, depth)
	case 0xde, 0xdf:
		n, err := r.count(2 << (t - 0xde))
		if err != nil {
			return nil, err
		}
		return r.msgpackMap(n, depth)
	}
	return nil, fmt.Errorf("0x%02x is not a MessagePack type", t)
}

func (r *reader) msgpackArray(n, depth int) (any, error) {
	out := make([]any, 0, min(n, preallocate))
	for range n {
		v, err := r.msgpack(depth + 1)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (r *reader) msgpackMap(n, depth int) (any, error) {
	m := &orderedMap{}
	for range n {
		k, err := r.msgpack(depth + 1)
		if err != nil {
			return nil, err
		}
		v, err := r.msgpack(depth + 1)
		if err != nil {
			return nil, err
		}
		m.set(keyString(k), v)
	}
	return m, nil
}

// msgpackExt reads an extension of n bytes: the timestamp, -1, as a time;
// another as its type with its bytes.
func (r *reader) msgpackExt(n int) (any, error) {
	t, err := r.byte()
	if err != nil {
		return nil, err
	}
	data, err := r.take(n)
	if err != nil {
		return nil, err
	}
	if int8(t) == -1 {
		switch n {
		case 4:
			return timestamp(int64(binary.BigEndian.Uint32(data)), 0), nil
		case 8:
			v := binary.BigEndian.Uint64(data)
			return timestamp(int64(v&0x3ffffffff), int64(v>>34)), nil
		case 12:
			return timestamp(int64(binary.BigEndian.Uint64(data[4:])), int64(binary.BigEndian.Uint32(data))), nil
		}
	}
	m := &orderedMap{}
	m.set("extension", int64(int8(t)))
	m.set("data", hex.EncodeToString(data))
	return m, nil
}

// timestamp is a time as JSON writes it, past the years it can hold as
// its seconds and nanoseconds since 1970.
func timestamp(secs, nanos int64) any {
	t := time.Unix(secs, nanos).UTC()
	if t.Year() < 0 || t.Year() > 9999 {
		return fmt.Sprintf("%d.%09d", secs, nanos)
	}
	return t.Format(time.RFC3339Nano)
}
