package decode

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// phpStart matches the start of a value PHP's serialize wrote.
var phpStart = regexp.MustCompile(`^(?:a:\d+:\{|s:\d+:"|i:-?\d+;|d:[-0-9.EINAF]+;|b:[01];|N;|O:\d+:"|C:\d+:"|E:\d+:")`)

func decodePHP(b []byte) (any, error) {
	r := &reader{b: b}
	v, err := r.php(0)
	if err != nil {
		return nil, err
	}
	return v, r.done()
}

// until reads up to a byte, which it passes over.
func (r *reader) until(end byte) (string, error) {
	i := bytes.IndexByte(r.b[r.pos:], end)
	if i < 0 {
		return "", errTruncated
	}
	s := string(r.b[r.pos : r.pos+i])
	r.pos += i + 1
	return s, nil
}

// expect passes over the bytes given, failing on others.
func (r *reader) expect(s string) error {
	got, err := r.take(len(s))
	if err != nil {
		return err
	}
	if string(got) != s {
		return fmt.Errorf("%q where %q was expected, at byte %d", got, s, r.pos-len(s))
	}
	return nil
}

// phpString reads LEN:"bytes", the quotes around them.
func (r *reader) phpString() (string, error) {
	n, err := r.phpCount(':')
	if err != nil {
		return "", err
	}
	if err := r.expect(`"`); err != nil {
		return "", err
	}
	s, err := r.take(n)
	if err != nil {
		return "", err
	}
	return string(s), r.expect(`"`)
}

// phpCount reads a count, up to the byte that ends it.
func (r *reader) phpCount(end byte) (int, error) {
	s, err := r.until(end)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > len(r.b)-r.pos {
		return 0, fmt.Errorf("%q is not a count", s)
	}
	return n, nil
}

func (r *reader) php(depth int) (any, error) {
	if depth > maxDepth {
		return nil, errors.New("the value nests too deep")
	}
	t, err := r.byte()
	if err != nil {
		return nil, err
	}
	if t == 'N' {
		return nil, r.expect(";")
	}
	if err := r.expect(":"); err != nil {
		return nil, err
	}
	switch t {
	case 'b':
		s, err := r.until(';')
		return s == "1", err
	case 'i':
		s, err := r.until(';')
		if err != nil {
			return nil, err
		}
		return strconv.ParseInt(s, 10, 64)
	case 'd':
		s, err := r.until(';')
		if err != nil {
			return nil, err
		}
		switch s {
		case "INF":
			return "INF", nil
		case "-INF":
			return "-INF", nil
		case "NAN":
			return "NAN", nil
		}
		f, err := strconv.ParseFloat(s, 64)
		if err == nil && (math.IsInf(f, 0) || math.IsNaN(f)) {
			return s, nil
		}
		return f, err
	case 's':
		s, err := r.phpString()
		if err != nil {
			return nil, err
		}
		return bytesValue(s), r.expect(";")
	case 'r', 'R':
		s, err := r.until(';')
		return "<reference to value " + s + ">", err
	case 'E':
		s, err := r.phpString()
		if err != nil {
			return nil, err
		}
		return "<enum " + s + ">", r.expect(";")
	case 'a':
		n, err := r.phpCount(':')
		if err != nil {
			return nil, err
		}
		return r.phpArray(n, depth, "")
	case 'O':
		class, err := r.phpString()
		if err != nil {
			return nil, err
		}
		if err := r.expect(":"); err != nil {
			return nil, err
		}
		n, err := r.phpCount(':')
		if err != nil {
			return nil, err
		}
		return r.phpArray(n, depth, class)
	case 'C':
		class, err := r.phpString()
		if err != nil {
			return nil, err
		}
		if err := r.expect(":"); err != nil {
			return nil, err
		}
		n, err := r.phpCount(':')
		if err != nil {
			return nil, err
		}
		if err := r.expect("{"); err != nil {
			return nil, err
		}
		data, err := r.take(n)
		if err != nil {
			return nil, err
		}
		m := &orderedMap{}
		m.set("__class", class)
		m.set("__serialized", bytesValue(data))
		return m, r.expect("}")
	}
	return nil, fmt.Errorf("%q is not a type of PHP's serialize", t)
}

// phpArray reads n keys and values within braces: an object's properties
// when class is set, else a list when its keys are 0 to n-1, else a map.
func (r *reader) phpArray(n, depth int, class string) (any, error) {
	if err := r.expect("{"); err != nil {
		return nil, err
	}
	m := &orderedMap{}
	if class != "" {
		m.set("__class", class)
	}
	list := class == ""
	for i := range n {
		k, err := r.php(depth + 1)
		if err != nil {
			return nil, err
		}
		v, err := r.php(depth + 1)
		if err != nil {
			return nil, err
		}
		if key, ok := k.(int64); !ok || key != int64(i) {
			list = false
		}
		m.set(phpKey(k), v)
	}
	if err := r.expect("}"); err != nil {
		return nil, err
	}
	if list {
		return m.values, nil
	}
	return m, nil
}

// phpKey writes an array's key, or a property's name without the marks
// of its visibility: \0*\0 protected, \0Class\0 private.
func phpKey(k any) string {
	s := keyString(k)
	if b, ok := k.(bytesValue); ok {
		s = string(b)
	}
	if strings.HasPrefix(s, "\x00") {
		if i := strings.IndexByte(s[1:], 0); i >= 0 {
			return s[i+2:]
		}
	}
	return s
}
