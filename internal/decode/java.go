package decode

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"unicode/utf16"
)

// The marks of Java's serialization stream.
const (
	tcNull           = 0x70
	tcReference      = 0x71
	tcClassDesc      = 0x72
	tcObject         = 0x73
	tcString         = 0x74
	tcArray          = 0x75
	tcClass          = 0x76
	tcBlockData      = 0x77
	tcEndBlockData   = 0x78
	tcReset          = 0x79
	tcBlockDataLong  = 0x7a
	tcException      = 0x7b
	tcLongString     = 0x7c
	tcProxyClassDesc = 0x7d
	tcEnum           = 0x7e

	baseHandle = 0x7e0000

	scWriteMethod    = 0x01
	scSerializable   = 0x02
	scExternalizable = 0x04
	scBlockData      = 0x08
)

type javaField struct {
	typ   byte // a primitive's code, or L or [ for an object
	name  string
	class string // an object field's class, as Java writes its type
}

type javaClass struct {
	name   string
	flags  byte
	fields []javaField
	super  *javaClass
}

type javaReader struct {
	reader
	handles []any
}

// decodeJava reads what Java's ObjectOutputStream wrote: an object as its
// class and its fields' values, superclasses' first, with what its class
// wrote of its own (as a HashMap's entries) as __written.
func decodeJava(b []byte) (any, error) {
	r := &javaReader{reader: reader{b: b}}
	if err := r.expect("\xac\xed\x00\x05"); err != nil {
		return nil, err
	}
	var out []any
	for r.pos < len(r.b) {
		v, err := r.content(0)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if len(out) == 1 {
		return out[0], nil
	}
	return out, nil
}

func (r *javaReader) handle(v any) int {
	r.handles = append(r.handles, v)
	return len(r.handles) - 1
}

// utf reads a string of modified UTF-8 after its length of n bytes.
func (r *javaReader) utf(n int) (string, error) {
	size, err := r.count(n)
	if err != nil {
		return "", err
	}
	b, err := r.take(size)
	if err != nil {
		return "", err
	}
	return modifiedUTF8(b), nil
}

// modifiedUTF8 decodes Java's UTF-8: NUL as two bytes, and characters past
// the 16 bits as UTF-16's surrogates, each written as three bytes.
func modifiedUTF8(b []byte) string {
	var units []uint16
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c < 0x80:
			units = append(units, uint16(c))
			i++
		case c&0xe0 == 0xc0 && i+1 < len(b):
			units = append(units, uint16(c&0x1f)<<6|uint16(b[i+1]&0x3f))
			i += 2
		case c&0xf0 == 0xe0 && i+2 < len(b):
			units = append(units, uint16(c&0x0f)<<12|uint16(b[i+1]&0x3f)<<6|uint16(b[i+2]&0x3f))
			i += 3
		default:
			units = append(units, 0xfffd)
			i++
		}
	}
	return string(utf16.Decode(units))
}

// content reads an object, or block data.
func (r *javaReader) content(depth int) (any, error) {
	if depth > maxDepth {
		return nil, errors.New("the value nests too deep")
	}
	t, err := r.byte()
	if err != nil {
		return nil, err
	}
	switch t {
	case tcNull:
		return nil, nil
	case tcReference:
		h, err := r.uint(4)
		if err != nil {
			return nil, err
		}
		i := int(h) - baseHandle
		if i < 0 || i >= len(r.handles) {
			return nil, fmt.Errorf("a reference to %#x, which the stream never made", h)
		}
		if _, ok := r.handles[i].(*javaClass); ok {
			return "<class " + r.handles[i].(*javaClass).name + ">", nil
		}
		return r.handles[i], nil
	case tcString, tcLongString:
		n := 2
		if t == tcLongString {
			n = 8
		}
		s, err := r.utf(n)
		if err != nil {
			return nil, err
		}
		r.handle(s)
		return s, nil
	case tcClassDesc, tcProxyClassDesc:
		r.pos--
		c, err := r.classDesc(depth)
		if err != nil || c == nil {
			return nil, err
		}
		return "<class " + c.name + ">", nil
	case tcClass:
		c, err := r.classDesc(depth)
		if err != nil {
			return nil, err
		}
		name := "?"
		if c != nil {
			name = c.name
		}
		r.handle("<class " + name + ">")
		return "<class " + name + ">", nil
	case tcEnum:
		c, err := r.classDesc(depth)
		if err != nil {
			return nil, err
		}
		if c == nil {
			return nil, errors.New("an enum without a class")
		}
		h := r.handle(nil)
		name, err := r.content(depth + 1)
		if err != nil {
			return nil, err
		}
		v := fmt.Sprintf("%s.%v", c.name, name)
		r.handles[h] = v
		return v, nil
	case tcArray:
		c, err := r.classDesc(depth)
		if err != nil {
			return nil, err
		}
		if c == nil || len(c.name) < 2 || c.name[0] != '[' {
			return nil, errors.New("an array without an array's class")
		}
		h := r.handle(nil)
		n, err := r.count(4)
		if err != nil {
			return nil, err
		}
		items := make([]any, 0, min(n, preallocate))
		for range n {
			v, err := r.fieldValue(c.name[1], depth)
			if err != nil {
				return nil, err
			}
			items = append(items, v)
		}
		r.handles[h] = items
		return items, nil
	case tcObject:
		c, err := r.classDesc(depth)
		if err != nil {
			return nil, err
		}
		if c == nil {
			return nil, errors.New("an object without a class")
		}
		m := &orderedMap{}
		m.set("__class", c.name)
		r.handle(m)
		// The superclasses' data first; a class among its own
		// superclasses is refused.
		var chain []*javaClass
		for k := c; k != nil; k = k.super {
			if len(chain) > maxDepth || slices.Contains(chain, k) {
				return nil, fmt.Errorf("%s is among its own superclasses, or under too many", c.name)
			}
			chain = append(chain, k)
		}
		slices.Reverse(chain)
		for _, k := range chain {
			if err := r.classData(k, m, depth); err != nil {
				return nil, fmt.Errorf("%s: %w", k.name, err)
			}
		}
		return m, nil
	case tcBlockData, tcBlockDataLong:
		n := 1
		if t == tcBlockDataLong {
			n = 4
		}
		size, err := r.count(n)
		if err != nil {
			return nil, err
		}
		b, err := r.take(size)
		return bytesValue(b), err
	case tcReset:
		// As many as there are, without a frame each.
		for r.pos < len(r.b) && r.b[r.pos] == tcReset {
			r.pos++
		}
		r.handles = nil
		return r.content(depth)
	case tcException:
		return nil, errors.New("the stream holds an exception thrown as it was written")
	}
	return nil, fmt.Errorf("0x%02x is not a mark of Java's serialization, at byte %d", t, r.pos-1)
}

// classDesc reads a class's description, nil for TC_NULL.
func (r *javaReader) classDesc(depth int) (*javaClass, error) {
	if depth > maxDepth {
		return nil, errors.New("the classes nest too deep")
	}
	t, err := r.byte()
	if err != nil {
		return nil, err
	}
	switch t {
	case tcNull:
		return nil, nil
	case tcReference:
		h, err := r.uint(4)
		if err != nil {
			return nil, err
		}
		if i := int(h) - baseHandle; i >= 0 && i < len(r.handles) {
			if c, ok := r.handles[i].(*javaClass); ok {
				return c, nil
			}
		}
		return nil, fmt.Errorf("a reference to %#x, which is not a class the stream described", h)
	case tcProxyClassDesc:
		c := &javaClass{name: "<proxy>"}
		r.handle(c)
		n, err := r.count(4)
		if err != nil {
			return nil, err
		}
		for range n {
			if _, err := r.utf(2); err != nil {
				return nil, err
			}
		}
		if err := r.annotations(depth, nil); err != nil {
			return nil, err
		}
		c.super, err = r.classDesc(depth + 1)
		return c, err
	case tcClassDesc:
		name, err := r.utf(2)
		if err != nil {
			return nil, err
		}
		if _, err := r.take(8); err != nil { // serialVersionUID
			return nil, err
		}
		c := &javaClass{name: name}
		r.handle(c)
		if c.flags, err = r.byte(); err != nil {
			return nil, err
		}
		n, err := r.count(2)
		if err != nil {
			return nil, err
		}
		for range n {
			var f javaField
			if f.typ, err = r.byte(); err != nil {
				return nil, err
			}
			if f.name, err = r.utf(2); err != nil {
				return nil, err
			}
			if f.typ == 'L' || f.typ == '[' {
				class, err := r.content(depth + 1)
				if err != nil {
					return nil, err
				}
				f.class = fmt.Sprint(class)
			}
			c.fields = append(c.fields, f)
		}
		if err := r.annotations(depth, nil); err != nil {
			return nil, err
		}
		c.super, err = r.classDesc(depth + 1)
		return c, err
	}
	return nil, fmt.Errorf("0x%02x where a class's description was expected", t)
}

// annotations reads what a class wrote of its own, up to TC_ENDBLOCKDATA,
// adding each to into when set.
func (r *javaReader) annotations(depth int, into *[]any) error {
	for {
		if r.pos >= len(r.b) {
			return errTruncated
		}
		if r.b[r.pos] == tcEndBlockData {
			r.pos++
			return nil
		}
		v, err := r.content(depth + 1)
		if err != nil {
			return err
		}
		if into != nil {
			*into = append(*into, v)
		}
	}
}

// classData reads an object's data of one class of its hierarchy into m.
func (r *javaReader) classData(c *javaClass, m *orderedMap, depth int) error {
	switch {
	case c.flags&scExternalizable != 0:
		if c.flags&scBlockData == 0 {
			return errors.New("written with the first version of the protocol, which only the class reads")
		}
		var written []any
		if err := r.annotations(depth, &written); err != nil {
			return err
		}
		m.set("__written", written)
	case c.flags&scSerializable != 0:
		for _, f := range c.fields {
			v, err := r.fieldValue(f.typ, depth)
			if err != nil {
				return fmt.Errorf("the field %s: %w", f.name, err)
			}
			m.set(f.name, v)
		}
		if c.flags&scWriteMethod != 0 {
			var written []any
			if err := r.annotations(depth, &written); err != nil {
				return err
			}
			if len(written) > 0 {
				m.set("__written", written)
			}
		}
	}
	return nil
}

// fieldValue reads a field's value, or an array's item, of a type code.
func (r *javaReader) fieldValue(typ byte, depth int) (any, error) {
	size := map[byte]int{'B': 1, 'C': 2, 'D': 8, 'F': 4, 'I': 4, 'J': 8, 'S': 2, 'Z': 1}[typ]
	if typ == 'L' || typ == '[' {
		return r.content(depth + 1)
	}
	if size == 0 {
		return nil, fmt.Errorf("%q is not a type of a field", typ)
	}
	v, err := r.uint(size)
	if err != nil {
		return nil, err
	}
	switch typ {
	case 'B':
		return int64(int8(v)), nil
	case 'C':
		return string(rune(v)), nil
	case 'D':
		return floatValue(math.Float64frombits(v)), nil
	case 'F':
		return floatValue(float64(math.Float32frombits(uint32(v)))), nil
	case 'I':
		return int64(int32(v)), nil
	case 'J':
		return int64(v), nil
	case 'S':
		return int64(int16(v)), nil
	}
	return v != 0, nil // Z
}
