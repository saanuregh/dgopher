package decode

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// decodeProtobuf reads a Protocol Buffers message without its schema, as
// protoc --decode_raw does: each field by its number, a nested message
// where its bytes read as one, else as text, else as bytes.
func decodeProtobuf(b []byte) (string, error) {
	if len(b) == 0 {
		return "", errors.New("an empty message")
	}
	work := maxScan
	if _, err := parseMessage(b, 0, &work); err != nil {
		return "", fmt.Errorf("not a Protocol Buffers message: %w", err)
	}
	out := &textWriter{}
	if err := writeMessage(out, b, "", 0, &work); err != nil && err != errTextFull {
		return "", fmt.Errorf("not a Protocol Buffers message: %w", err)
	}
	return out.text(), nil
}

// protoField is a field as the wire holds it.
type protoField struct {
	number uint64
	wire   uint64
	value  uint64 // a varint's or a fixed number's
	bytes  []byte // a length-delimited field's, or a group's
}

// maxScan is how many bytes a message's reading scans in all: each
// nested message is read again, which a deep one makes slow.
const maxScan = 64 << 20

// parseMessage reads a message's fields, failing unless they take all of
// its bytes; work counts the bytes scanned, down to none.
func parseMessage(b []byte, depth int, work *int) ([]protoField, error) {
	if depth > maxDepth {
		return nil, errors.New("the message nests too deep")
	}
	if *work -= len(b); *work < 0 {
		return nil, errors.New("the message is too large or nests too deep to read")
	}
	var fields []protoField
	for len(b) > 0 {
		tag, n := binary.Uvarint(b)
		if n <= 0 {
			return nil, errors.New("a field's tag is not a varint")
		}
		b = b[n:]
		f := protoField{number: tag >> 3, wire: tag & 7}
		if f.number == 0 || f.number > 1<<29-1 {
			return nil, fmt.Errorf("the field number %d", f.number)
		}
		switch f.wire {
		case 0:
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return nil, errTruncated
			}
			f.value, b = v, b[n:]
		case 1:
			if len(b) < 8 {
				return nil, errTruncated
			}
			f.value, b = binary.LittleEndian.Uint64(b), b[8:]
		case 5:
			if len(b) < 4 {
				return nil, errTruncated
			}
			f.value, b = uint64(binary.LittleEndian.Uint32(b)), b[4:]
		case 2:
			size, n := binary.Uvarint(b)
			if n <= 0 || size > uint64(len(b)-n) {
				return nil, errTruncated
			}
			f.bytes, b = b[n:n+int(size)], b[n+int(size):]
		case 3:
			// A group: its fields up to the end group of its number.
			end, err := groupEnd(b, f.number, depth)
			if err != nil {
				return nil, err
			}
			f.bytes, b = b[:end.start], b[end.next:]
		default:
			return nil, fmt.Errorf("the wire type %d", f.wire)
		}
		fields = append(fields, f)
	}
	return fields, nil
}

type groupBounds struct{ start, next int }

// groupEnd finds the end group of a group's number, past the fields
// inside it.
func groupEnd(b []byte, number uint64, depth int) (groupBounds, error) {
	if depth > maxDepth {
		return groupBounds{}, errors.New("the groups nest too deep")
	}
	for i := 0; i < len(b); {
		tag, n := binary.Uvarint(b[i:])
		if n <= 0 {
			return groupBounds{}, errTruncated
		}
		if tag>>3 == number && tag&7 == 4 {
			return groupBounds{i, i + n}, nil
		}
		i += n
		switch tag & 7 {
		case 0:
			_, n := binary.Uvarint(b[i:])
			if n <= 0 {
				return groupBounds{}, errTruncated
			}
			i += n
		case 1:
			i += 8
		case 5:
			i += 4
		case 2:
			size, n := binary.Uvarint(b[i:])
			if n <= 0 || size > uint64(len(b)-i-n) {
				return groupBounds{}, errTruncated
			}
			i += n + int(size)
		case 3:
			inner, err := groupEnd(b[i:], tag>>3, depth+1)
			if err != nil {
				return groupBounds{}, err
			}
			i += inner.next
		default:
			return groupBounds{}, fmt.Errorf("the wire type %d", tag&7)
		}
	}
	return groupBounds{}, errors.New("a group without its end")
}

func writeMessage(out *textWriter, b []byte, indent string, depth int, work *int) error {
	fields, err := parseMessage(b, depth, work)
	if err != nil {
		return err
	}
	for _, f := range fields {
		if out.full {
			return errTextFull
		}
		out.WriteString(indent + strconv.FormatUint(f.number, 10))
		switch f.wire {
		case 0:
			fmt.Fprintf(out, ": %d\n", f.value)
		case 1:
			fmt.Fprintf(out, ": 0x%016x\n", f.value)
		case 5:
			fmt.Fprintf(out, ": 0x%08x\n", f.value)
		case 2, 3:
			// A nested message where it reads as one, whole.
			if _, err := parseMessage(f.bytes, depth+1, work); err == nil && len(f.bytes) > 0 && (f.wire == 3 || !printable(f.bytes)) {
				out.WriteString(" {\n")
				if err := writeMessage(out, f.bytes, indent+"  ", depth+1, work); err != nil {
					return err
				}
				out.WriteString(indent + "}\n")
				continue
			}
			out.WriteString(": " + quoteBytes(f.bytes) + "\n")
		}
	}
	return nil
}

// printable reports whether bytes are text a person reads, which a
// message's bytes seldom are.
func printable(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}
	for _, r := range string(b) {
		if !unicode.IsPrint(r) && !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// quoteBytes writes bytes as protoc does: text in quotes, other bytes
// escaped in octal.
func quoteBytes(b []byte) string {
	text := utf8.Valid(b)
	var s strings.Builder
	s.WriteByte('"')
	for _, c := range b {
		switch {
		case c == '"' || c == '\\':
			s.WriteByte('\\')
			s.WriteByte(c)
		case c == '\n':
			s.WriteString(`\n`)
		case c >= 0x20 && c < 0x7f || c >= 0x80 && text:
			s.WriteByte(c)
		default:
			fmt.Fprintf(&s, `\%03o`, c)
		}
	}
	s.WriteByte('"')
	return s.String()
}
