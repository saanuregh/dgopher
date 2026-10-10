package decode

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Python's values while unpickling: a list, a dict, a set or an object
// are changed after they are made, and may be shared, so they are
// pointers until the value is whole.
type (
	pyList   struct{ items []any }
	pyDict   struct{ keys, values []any }
	pySet    struct{ items []any }
	pyGlobal struct{ module, name string }
	pyObject struct {
		class string
		args  any
		state any
	}
)

// pyMark is a mark on the unpickler's stack.
type pyMark struct{}

// decodePickle reads a pickle as the values it holds, never running the
// code it names: an object is its class with its arguments and state.
func decodePickle(b []byte) (any, error) {
	r := &reader{b: b}
	var stack []any
	memo := map[int64]any{}
	encoded := 0
	pop := func() (any, error) {
		if len(stack) == 0 {
			return nil, errors.New("the pickle takes from an empty stack")
		}
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		return v, nil
	}
	popMark := func() ([]any, error) {
		for i := len(stack) - 1; i >= 0; i-- {
			if _, ok := stack[i].(pyMark); ok {
				items := append([]any(nil), stack[i+1:]...)
				stack = stack[:i]
				return items, nil
			}
		}
		return nil, errors.New("the pickle takes to a mark it never set")
	}
	top := func() (any, error) {
		if len(stack) == 0 {
			return nil, errors.New("the pickle reads an empty stack")
		}
		return stack[len(stack)-1], nil
	}
	line := func() (string, error) {
		s, err := r.until('\n')
		return strings.TrimSuffix(s, "\r"), err
	}
	// Pickle's numbers are little-endian.
	le := func(n int) (uint64, error) {
		b, err := r.take(n)
		if err != nil {
			return 0, err
		}
		var v uint64
		for i := n - 1; i >= 0; i-- {
			v = v<<8 | uint64(b[i])
		}
		return v, nil
	}
	// sized reads bytes after their length, of n bytes.
	sized := func(n int) ([]byte, error) {
		size, err := le(n)
		if err != nil {
			return nil, err
		}
		if size > uint64(len(r.b)-r.pos) {
			return nil, errTruncated
		}
		return r.take(int(size))
	}
	for {
		op, err := r.byte()
		if err != nil {
			return nil, err
		}
		switch op {
		case 0x80: // PROTO
			if _, err := r.byte(); err != nil {
				return nil, err
			}
		case 0x95: // FRAME
			if _, err := r.take(8); err != nil {
				return nil, err
			}
		case '.': // STOP
			v, err := pop()
			if err != nil {
				return nil, err
			}
			if err := r.done(); err != nil {
				return nil, err
			}
			w := newWalk()
			w.names = &r.names
			return plain(v, w)
		case '(':
			stack = append(stack, pyMark{})
		case '0':
			if _, err := pop(); err != nil {
				return nil, err
			}
		case '1':
			if _, err := popMark(); err != nil {
				return nil, err
			}
		case '2':
			v, err := top()
			if err != nil {
				return nil, err
			}
			stack = append(stack, v)
		case 'N':
			stack = append(stack, nil)
		case 0x88:
			stack = append(stack, true)
		case 0x89:
			stack = append(stack, false)
		case 'I':
			s, err := line()
			if err != nil {
				return nil, err
			}
			switch s {
			case "01":
				stack = append(stack, true)
			case "00":
				stack = append(stack, false)
			default:
				n, err := strconv.ParseInt(s, 10, 64)
				if err != nil {
					return nil, err
				}
				stack = append(stack, n)
			}
		case 'J':
			v, err := le(4)
			if err != nil {
				return nil, err
			}
			stack = append(stack, int64(int32(v)))
		case 'K':
			v, err := r.byte()
			if err != nil {
				return nil, err
			}
			stack = append(stack, int64(v))
		case 'M':
			v, err := le(2)
			if err != nil {
				return nil, err
			}
			stack = append(stack, int64(v))
		case 'L':
			s, err := line()
			if err != nil {
				return nil, err
			}
			if len(s) > maxDigits {
				return nil, fmt.Errorf("a number of more than %d digits", maxDigits)
			}
			n, ok := new(big.Int).SetString(strings.TrimSuffix(s, "L"), 10)
			if !ok {
				return nil, fmt.Errorf("%q is not a number", s)
			}
			stack = append(stack, bigValue(n))
		case 0x8a, 0x8b: // LONG1, LONG4
			size := 1
			if op == 0x8b {
				size = 4
			}
			b, err := sized(size)
			if err != nil {
				return nil, err
			}
			if len(b) > maxDigits/2 {
				return nil, fmt.Errorf("a number of more than %d bytes", maxDigits/2)
			}
			stack = append(stack, bigValue(littleSigned(b)))
		case 'F':
			s, err := line()
			if err != nil {
				return nil, err
			}
			f, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return nil, err
			}
			stack = append(stack, floatValue(f))
		case 'G':
			v, err := r.uint(8)
			if err != nil {
				return nil, err
			}
			stack = append(stack, floatValue(math.Float64frombits(v)))
		case 'S':
			s, err := line()
			if err != nil {
				return nil, err
			}
			u, err := strconv.Unquote(pyQuoted(s))
			if err != nil {
				return nil, fmt.Errorf("the string %s: %w", s, err)
			}
			stack = append(stack, bytesValue(u))
		case 'T', 'U', 'B', 'C', 0x8e, 0x96: // strings of Python 2, bytes, bytearray
			size := map[byte]int{'T': 4, 'U': 1, 'B': 4, 'C': 1, 0x8e: 8, 0x96: 8}[op]
			b, err := sized(size)
			if err != nil {
				return nil, err
			}
			stack = append(stack, bytesValue(b))
		case 'V':
			s, err := line()
			if err != nil {
				return nil, err
			}
			stack = append(stack, rawUnicode(s))
		case 'X', 0x8c, 0x8d: // BINUNICODE, SHORT_BINUNICODE, BINUNICODE8
			size := map[byte]int{'X': 4, 0x8c: 1, 0x8d: 8}[op]
			b, err := sized(size)
			if err != nil {
				return nil, err
			}
			if !utf8.Valid(b) {
				return nil, errors.New("a string that is not UTF-8")
			}
			stack = append(stack, string(b))
		case ']':
			stack = append(stack, &pyList{})
		case 'l':
			items, err := popMark()
			if err != nil {
				return nil, err
			}
			stack = append(stack, &pyList{items: items})
		case 'a', 'e':
			var items []any
			if op == 'a' {
				v, err := pop()
				if err != nil {
					return nil, err
				}
				items = []any{v}
			} else if items, err = popMark(); err != nil {
				return nil, err
			}
			t, err := top()
			if err != nil {
				return nil, err
			}
			switch c := t.(type) {
			case *pyList:
				c.items = append(c.items, items...)
			case *pyObject:
				// A list's subclass: its items, kept as its state.
				c.state = appendItems(c.state, items)
			default:
				return nil, fmt.Errorf("APPEND to a %T", t)
			}
		case ')':
			stack = append(stack, []any{})
		case 't':
			items, err := popMark()
			if err != nil {
				return nil, err
			}
			stack = append(stack, items)
		case 0x85, 0x86, 0x87: // TUPLE1, TUPLE2, TUPLE3
			n := int(op - 0x84)
			if len(stack) < n {
				return nil, errors.New("the pickle takes from an empty stack")
			}
			items := append([]any(nil), stack[len(stack)-n:]...)
			stack = append(stack[:len(stack)-n], items)
		case '}':
			stack = append(stack, &pyDict{})
		case 'd':
			items, err := popMark()
			if err != nil {
				return nil, err
			}
			d := &pyDict{}
			if err := d.add(items); err != nil {
				return nil, err
			}
			stack = append(stack, d)
		case 's', 'u':
			var items []any
			if op == 's' {
				v, err := pop()
				if err != nil {
					return nil, err
				}
				k, err := pop()
				if err != nil {
					return nil, err
				}
				items = []any{k, v}
			} else if items, err = popMark(); err != nil {
				return nil, err
			}
			t, err := top()
			if err != nil {
				return nil, err
			}
			switch c := t.(type) {
			case *pyDict:
				if err := c.add(items); err != nil {
					return nil, err
				}
			case *pyObject:
				d, _ := c.state.(*pyDict)
				if d == nil {
					d = &pyDict{}
					c.state = d
				}
				if err := d.add(items); err != nil {
					return nil, err
				}
			default:
				return nil, fmt.Errorf("SETITEM on a %T", t)
			}
		case 0x8f:
			stack = append(stack, &pySet{})
		case 0x90:
			items, err := popMark()
			if err != nil {
				return nil, err
			}
			t, err := top()
			if err != nil {
				return nil, err
			}
			s, ok := t.(*pySet)
			if !ok {
				return nil, fmt.Errorf("ADDITEMS to a %T", t)
			}
			s.items = append(s.items, items...)
		case 0x91:
			items, err := popMark()
			if err != nil {
				return nil, err
			}
			stack = append(stack, &pySet{items: items})
		case 'p', 'q', 'r', 0x94: // PUT, BINPUT, LONG_BINPUT, MEMOIZE
			var key int64
			switch op {
			case 'p':
				s, err := line()
				if err != nil {
					return nil, err
				}
				if key, err = strconv.ParseInt(s, 10, 64); err != nil {
					return nil, err
				}
			case 'q':
				v, err := r.byte()
				if err != nil {
					return nil, err
				}
				key = int64(v)
			case 'r':
				v, err := le(4)
				if err != nil {
					return nil, err
				}
				key = int64(v)
			default:
				key = int64(len(memo))
			}
			v, err := top()
			if err != nil {
				return nil, err
			}
			memo[key] = v
		case 'g', 'h', 'j': // GET, BINGET, LONG_BINGET
			var key int64
			switch op {
			case 'g':
				s, err := line()
				if err != nil {
					return nil, err
				}
				if key, err = strconv.ParseInt(s, 10, 64); err != nil {
					return nil, err
				}
			case 'h':
				v, err := r.byte()
				if err != nil {
					return nil, err
				}
				key = int64(v)
			default:
				v, err := le(4)
				if err != nil {
					return nil, err
				}
				key = int64(v)
			}
			v, ok := memo[key]
			if !ok {
				return nil, fmt.Errorf("the pickle reads %d, which it never kept", key)
			}
			stack = append(stack, v)
		case 'c':
			module, err := line()
			if err != nil {
				return nil, err
			}
			name, err := line()
			if err != nil {
				return nil, err
			}
			stack = append(stack, pyGlobal{module, name})
		case 0x93: // STACK_GLOBAL
			name, err := pop()
			if err != nil {
				return nil, err
			}
			module, err := pop()
			if err != nil {
				return nil, err
			}
			stack = append(stack, pyGlobal{r.names.name(module), r.names.name(name)})
		case 'R', 0x81, 0x92: // REDUCE, NEWOBJ, NEWOBJ_EX
			if op == 0x92 {
				if _, err := pop(); err != nil { // the keyword arguments
					return nil, err
				}
			}
			args, err := pop()
			if err != nil {
				return nil, err
			}
			callable, err := pop()
			if err != nil {
				return nil, err
			}
			v, err := construct(className(callable, &r.names), args, &encoded)
			if err != nil {
				return nil, err
			}
			stack = append(stack, v)
		case 'b': // BUILD
			state, err := pop()
			if err != nil {
				return nil, err
			}
			t, err := top()
			if err != nil {
				return nil, err
			}
			if o, ok := t.(*pyObject); ok {
				o.state = state
			}
		case 'P', 'Q', 0x82, 0x83, 0x84: // persistent IDs and extensions
			return nil, errors.New("the pickle refers to objects outside it, which only its program can read")
		default:
			return nil, fmt.Errorf("0x%02x is not an opcode of pickle", op)
		}
	}
}

// maxEncoded is how many bytes the _codecs.encode reductions of one
// pickle make together at most: each copies its text, which the memo may
// hand to any number of them.
const maxEncoded = 64 << 20

// construct makes the value a class makes of its arguments: the values
// of the classes the older protocols write bytes, sets and ordered dicts
// with, else an object of the class. encoded counts the bytes made.
func construct(class string, args any, encoded *int) (any, error) {
	list, _ := args.([]any)
	switch class {
	case "_codecs.encode":
		// Bytes, of protocol 0 to 2: their Latin-1 text.
		if len(list) == 2 {
			if s, ok := list[0].(string); ok && list[1] == "latin1" {
				if *encoded += len(s); *encoded > maxEncoded {
					return nil, fmt.Errorf("the pickle makes more than %d MB of bytes", maxEncoded>>20)
				}
				b := make([]byte, 0, len(s))
				for _, r := range s {
					b = append(b, byte(r))
				}
				return bytesValue(b), nil
			}
		}
	case "builtins.set", "__builtin__.set", "builtins.frozenset", "__builtin__.frozenset":
		if len(list) == 1 {
			if l, ok := list[0].(*pyList); ok {
				return &pySet{items: l.items}, nil
			}
		}
	case "collections.OrderedDict":
		if len(list) == 0 {
			return &pyDict{}, nil
		}
	}
	return &pyObject{class: class, args: args}, nil
}

func (d *pyDict) add(items []any) error {
	if len(items)%2 != 0 {
		return errors.New("a dict's keys without their values")
	}
	for i := 0; i < len(items); i += 2 {
		d.keys, d.values = append(d.keys, items[i]), append(d.values, items[i+1])
	}
	return nil
}

func appendItems(state any, items []any) any {
	l, _ := state.(*pyList)
	if l == nil {
		l = &pyList{}
	}
	l.items = append(l.items, items...)
	return l
}

func className(v any, n *names) string {
	if g, ok := v.(pyGlobal); ok {
		return n.name(g.module, ".", g.name)
	}
	return n.name(v)
}

// plain turns the unpickled values into those JSON writes: a list, a set
// or a tuple as an array, a dict as an object, an object as its class with
// its arguments and state; a value inside itself as a mark of it. Values
// shared count each time they show, up to maxValues.
func plain(v any, w *walk) (any, error) {
	// Only a pointer can be around itself, and a slice is not a map's key.
	var self any
	switch v.(type) {
	case *pyList, *pyDict, *pySet, *pyObject:
		self = v
	}
	recursive, err := w.enter(self)
	if err != nil {
		return nil, err
	}
	switch x := v.(type) {
	case *pyList, *pyDict, *pySet, *pyObject:
		if recursive {
			return "<recursion>", nil
		}
		w.onPath[x] = true
		defer delete(w.onPath, x)
	}
	items := func(vs []any) ([]any, error) {
		out := make([]any, len(vs))
		for i, item := range vs {
			var err error
			if out[i], err = plain(item, w); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	switch x := v.(type) {
	case *pyList:
		return items(x.items)
	case *pySet:
		return items(x.items)
	case []any:
		return items(x)
	case *pyDict:
		m := &orderedMap{}
		for i, k := range x.keys {
			key, err := plain(k, w)
			if err != nil {
				return nil, err
			}
			value, err := plain(x.values[i], w)
			if err != nil {
				return nil, err
			}
			m.set(w.names.keyString(key), value)
		}
		return m, nil
	case *pyObject:
		m := &orderedMap{}
		m.set("__class", x.class)
		if args, ok := x.args.([]any); !ok || len(args) > 0 {
			a, err := plain(x.args, w)
			if err != nil {
				return nil, err
			}
			m.set("args", a)
		}
		if x.state != nil {
			s, err := plain(x.state, w)
			if err != nil {
				return nil, err
			}
			m.set("state", s)
		}
		return m, nil
	case pyGlobal:
		return w.names.name("<", x.module, ".", x.name, ">"), nil
	}
	return v, nil
}

// maxDigits is how long a number may be, as Python limits its text:
// longer ones convert slowly.
const maxDigits = 4300

// littleSigned reads a little-endian two's-complement number.
func littleSigned(b []byte) *big.Int {
	be := make([]byte, len(b))
	for i, c := range b {
		be[len(b)-1-i] = c
	}
	n := new(big.Int).SetBytes(be)
	if len(b) > 0 && b[len(b)-1]&0x80 != 0 {
		n.Sub(n, new(big.Int).Lsh(big.NewInt(1), uint(8*len(b))))
	}
	return n
}

// bigValue is a number as JSON writes it: an int64 where it fits, else
// its digits, which JSON's numbers would round.
func bigValue(n *big.Int) any {
	if n.IsInt64() {
		return n.Int64()
	}
	return n.String()
}

// floatValue is a float as JSON writes it: infinities and NaN as text.
func floatValue(f float64) any {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	return f
}

// pyQuoted turns a Python string literal, 'x' or "x", into Go's.
func pyQuoted(s string) string {
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		inner := strings.ReplaceAll(s[1:len(s)-1], `\'`, `'`)
		return `"` + strings.ReplaceAll(inner, `"`, `\"`) + `"`
	}
	return s
}

// rawUnicode reads Python's raw-unicode-escape: \uXXXX and \UXXXXXXXX
// escapes, other bytes as Latin-1.
func rawUnicode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && (s[i+1] == 'u' || s[i+1] == 'U') {
			n := 4
			if s[i+1] == 'U' {
				n = 8
			}
			if i+2+n <= len(s) {
				if r, err := strconv.ParseUint(s[i+2:i+2+n], 16, 32); err == nil {
					b.WriteRune(rune(r))
					i += 1 + n
					continue
				}
			}
		}
		b.WriteRune(rune(s[i]))
	}
	return b.String()
}
