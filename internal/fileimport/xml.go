package fileimport

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"dgopher/internal/export"
)

// xmlMaxDepth is how deep an XML file's rows are looked for.
const xmlMaxDepth = 8

// How deep the elements of an XML file, or of a workbook's part, may
// nest, and how many name spaces the elements open at once may declare:
// the XML decoder holds each of them until its element ends.
const (
	maxElementDepth   = 10_000
	maxOpenNamespaces = 10_000
)

// newTokenReader reads the tokens of r as xml.Decoder's Token does,
// refusing, as name, elements past nestingLimit's limits.
func newTokenReader(r io.Reader, name string) xml.TokenReader {
	return &nestingLimit{next: xml.NewDecoder(&markupLimit{r: r, name: name}).Token, name: name}
}

// newDecoder is xml.NewDecoder(r) refusing, as name, elements past
// nestingLimit's limits. It reads raw tokens, so that only the decoder
// returned translates name spaces and matches end elements; its errors
// for an end element that does not match, or is missing, say line 1.
func newDecoder(r io.Reader, name string) *xml.Decoder {
	return xml.NewTokenDecoder(&nestingLimit{next: xml.NewDecoder(&markupLimit{r: r, name: name}).RawToken, name: name})
}

// markupLimit reads r, refusing, as name, markup longer than
// maxTokenSize from its '<' to its end, which the XML decoder holds whole
// with all its attributes before nestingLimit could count them. Comments,
// CDATA sections, processing instructions and directives count too: the
// decoder holds each whole as one token as well. Text between markup,
// which the decoder holds whole as one token too, is refused past
// maxTokenSize as well.
type markupLimit struct {
	r    io.Reader
	name string
	// The markup being read, if any: its kind, its bytes so far, the quote
	// a value is open in, the depth of '<' in a directive, and the last
	// bytes seen, which a comment's, CDATA's or instruction's end needs.
	kind  markupKind
	size  int
	attrs int // the attributes of a tag
	quote byte
	depth int
	last  [2]byte
	// In a directive: how much of "<!--" was just read, whether a comment
	// is open in it, and that comment's last bytes.
	opening     int
	inComment   bool
	commentLast [2]byte
	text        int // the bytes of text since the last markup
}

// maxTagAttributes is the most attributes a tag may have: the decoder
// takes some 25 bytes of memory for each byte of a tag of many short
// attributes, so maxTokenSize alone would let one tag take 25 MB. A row
// takes at most export.ExcelMaxColumns columns anyway.
const maxTagAttributes = export.ExcelMaxColumns

type markupKind int

const (
	markupNone markupKind = iota
	markupOpen            // after '<', its kind not yet known
	markupTag
	markupBang // after "<!", a comment, CDATA or directive not yet known
	markupComment
	markupCDATA
	markupInstruction
	markupDirective
)

func (m *markupLimit) Read(b []byte) (int, error) {
	n, err := m.r.Read(b)
	for _, c := range b[:n] {
		if m.kind == markupNone {
			if c == '<' {
				m.kind, m.size, m.attrs, m.quote, m.depth, m.last = markupOpen, 1, 0, 0, 1, [2]byte{}
				m.opening, m.inComment, m.text = 0, false, 0
			} else if m.text++; m.text > maxTokenSize {
				return n, fmt.Errorf("%s holds a value longer than %d KB", m.name, maxTokenSize>>10)
			}
			continue
		}
		if m.size++; m.size > maxTokenSize {
			return n, fmt.Errorf("%s holds a tag longer than %d KB", m.name, maxTokenSize>>10)
		}
		switch m.kind {
		case markupOpen:
			switch c {
			case '?':
				m.kind = markupInstruction
			case '!':
				m.kind = markupBang
			default:
				m.kind = markupTag
			}
		case markupBang:
			// "<!-" starts a comment, "<![" CDATA, anything else a directive.
			switch c {
			case '-':
				m.kind = markupComment
			case '[':
				m.kind = markupCDATA
			default:
				m.kind = markupDirective
				m.directiveByte(c)
			}
		case markupTag:
			if m.tagByte(c); m.attrs > maxTagAttributes {
				return n, fmt.Errorf("%s holds a tag of more than %d attributes", m.name, maxTagAttributes)
			}
		case markupComment:
			// The '-' after "<!" is not one of the closing "--".
			if c == '>' && m.last == [2]byte{'-', '-'} && m.size > 6 {
				m.kind = markupNone
			}
		case markupCDATA:
			if c == '>' && m.last == [2]byte{']', ']'} {
				m.kind = markupNone
			}
		case markupInstruction:
			if c == '>' && m.last[1] == '?' && m.size > 3 {
				m.kind = markupNone
			}
		case markupDirective:
			m.directiveByte(c)
		}
		m.last = [2]byte{m.last[1], c}
	}
	return n, err
}

// tagByte reads a byte of a start or end tag, which a '>' outside a
// quoted value ends, counting its attributes by their '='.
func (m *markupLimit) tagByte(c byte) {
	switch {
	case m.quote == 0 && c == '=':
		m.attrs++
	case m.quote != 0:
		if c == m.quote {
			m.quote = 0
		}
	case c == '"' || c == '\'':
		m.quote = c
	case c == '>':
		m.kind = markupNone
	}
}

// directiveByte reads a byte of a directive, as <!DOCTYPE …>, which
// holds quoted values, comments and markup of its own, nested in '<' and
// '>', as the XML decoder reads it: a comment, from "<!--" outside quotes
// to "-->", is skipped, whatever it holds.
func (m *markupLimit) directiveByte(c byte) {
	if m.inComment {
		if c == '>' && m.commentLast == [2]byte{'-', '-'} {
			m.inComment = false
		}
		m.commentLast = [2]byte{m.commentLast[1], c}
		return
	}
	if m.opening > 0 {
		if c == "<!--"[m.opening] {
			if m.opening++; m.opening == 4 {
				m.opening, m.inComment, m.commentLast = 0, true, [2]byte{}
			}
			return
		}
		// Not a comment: the '<' nests, and c is read as any other byte.
		m.opening = 0
		m.depth++
	}
	switch {
	case m.quote != 0:
		if c == m.quote {
			m.quote = 0
		}
	case c == '"' || c == '\'':
		m.quote = c
	case c == '<':
		m.opening = 1
	case c == '>':
		if m.depth--; m.depth == 0 {
			m.kind = markupNone
		}
	}
}

// nestingLimit passes on the tokens next reads until elements nest
// deeper than maxElementDepth, or those open declare more than
// maxOpenNamespaces name spaces, before a decoder holds more of them.
type nestingLimit struct {
	next     func() (xml.Token, error)
	name     string
	declared []int // the name spaces each open element declares
	spaces   int   // their sum
}

func (l *nestingLimit) Token() (xml.Token, error) {
	t, err := l.next()
	switch t := t.(type) {
	case xml.StartElement:
		n := 0
		for _, a := range t.Attr {
			if a.Name.Space == "xmlns" || a.Name.Space == "" && a.Name.Local == "xmlns" {
				n++
			}
		}
		l.declared, l.spaces = append(l.declared, n), l.spaces+n
		if len(l.declared) > maxElementDepth {
			return nil, fmt.Errorf("%s nests elements deeper than %d", l.name, maxElementDepth)
		}
		if l.spaces > maxOpenNamespaces {
			return nil, fmt.Errorf("%s declares more than %d name spaces in elements open at once", l.name, maxOpenNamespaces)
		}
	case xml.EndElement:
		// Raw tokens may end an element that never started.
		if k := len(l.declared); k > 0 {
			l.spaces -= l.declared[k-1]
			l.declared = l.declared[:k-1]
		}
	}
	return t, err
}

// loadXML reads the rows of an XML file into the table src: each element
// at the rows' path is a row, whose attributes and child elements are its
// columns. A child repeated in a row is a JSON array of its texts, and a
// child with children of its own gives its text.
func (f *File) loadXML(ctx context.Context, opt Options) error {
	f.Rows = opt.Rows
	if f.Rows == "" {
		path, err := mostRepeated(f.Path)
		if err != nil {
			return err
		}
		f.Rows = path
	}
	// The columns first, then the rows, read again, straight into the
	// table: a file's rows held all at once take many times its size.
	var names []string
	index := map[string]int{}
	found := false
	err := xmlRows(f.Path, f.Rows, func(fields map[string][]string, order []string) error {
		found = true
		for _, n := range order {
			if _, ok := index[n]; !ok {
				if len(names) == export.ExcelMaxColumns {
					return fmt.Errorf("the elements %s hold more than %d attributes and child elements; an import takes at most %d columns", f.Rows, export.ExcelMaxColumns, export.ExcelMaxColumns)
				}
				index[n] = len(names)
				names = append(names, n)
			}
		}
		return ctx.Err()
	})
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("no element %s in the file", f.Rows)
	}
	if len(names) == 0 {
		return fmt.Errorf("the elements %s hold no attributes or child elements", f.Rows)
	}
	f.Columns = make([]Column, len(names))
	for i, n := range uniqueNames(names) {
		f.Columns[i] = Column{Name: n, Type: "VARCHAR"}
	}
	err = f.createAndAppend(ctx, func(add func([]any) error) error {
		return xmlRows(f.Path, f.Rows, func(fields map[string][]string, _ []string) error {
			vals := make([]any, len(names))
			for n, vs := range fields {
				i, ok := index[n]
				if !ok {
					return fmt.Errorf("the file changed while it was read")
				}
				switch len(vs) {
				case 1:
					vals[i] = vs[0]
				default:
					out, _ := json.Marshal(vs)
					vals[i] = string(out)
				}
			}
			return add(vals)
		})
	})
	if err != nil || opt.AllText {
		return err
	}
	return f.sniffTypes(ctx)
}

// mostRepeated is the path, under the root, of the element an XML file
// repeats most among those with attributes or child elements, which a
// row has, the shallowest of those repeated as often: its rows. The
// fields of a row, repeated as often or more, hold only text.
func mostRepeated(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	counts := map[string]int{}
	var stack []string
	var structured []bool // whether each open element has attributes or children
	d := newTokenReader(file, filepath.Base(path))
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("reading the XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(structured) > 0 {
				structured[len(structured)-1] = true
			}
			stack = append(stack, t.Name.Local)
			structured = append(structured, len(t.Attr) > 0)
		case xml.EndElement:
			if n := len(stack); n > 1 && n <= xmlMaxDepth && structured[n-1] {
				counts[strings.Join(stack[1:], "/")]++
			}
			stack, structured = stack[:len(stack)-1], structured[:len(structured)-1]
		}
	}
	best, most := "", 0
	for p, n := range counts {
		depth, bestDepth := strings.Count(p, "/"), strings.Count(best, "/")
		if n > most || n == most && (depth < bestDepth || depth == bestDepth && p < best) {
			best, most = p, n
		}
	}
	if best == "" {
		return "", fmt.Errorf("the XML has no elements under its root to read as rows")
	}
	return best, nil
}

// xmlRows calls each with the fields of every element at path under the
// root, by name, and the names in the order they came.
func xmlRows(path, rows string, each func(fields map[string][]string, order []string) error) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	want := strings.Split(strings.Trim(rows, "/"), "/")
	var stack []string
	var fields map[string][]string
	var order []string
	add := func(name, v string) {
		if _, ok := fields[name]; !ok {
			order = append(order, name)
		}
		fields[name] = append(fields[name], v)
	}
	// While in a row: the child being read, and its text.
	child, depth := "", 0
	var text strings.Builder
	d := newTokenReader(file, filepath.Base(path))
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading the XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, t.Name.Local)
			switch {
			case fields == nil && atPath(stack, want):
				fields, order = map[string][]string{}, nil
				for _, a := range t.Attr {
					add(a.Name.Local, a.Value)
				}
			case fields != nil && child == "":
				child, depth = t.Name.Local, len(stack)
				text.Reset()
				for _, a := range t.Attr {
					add(child+"_"+a.Name.Local, a.Value)
				}
			}
		case xml.CharData:
			if child != "" {
				if text.Len() > 0 && len(stack) > depth {
					text.WriteByte(' ') // the texts of grandchildren apart
				}
				if text.Len()+len(t) > maxTokenSize {
					return fmt.Errorf("%s holds a value longer than %d KB in the element %s", filepath.Base(path), maxTokenSize>>10, child)
				}
				text.Write(t)
			}
		case xml.EndElement:
			switch {
			case child != "" && len(stack) == depth:
				add(child, strings.TrimSpace(text.String()))
				child = ""
			case fields != nil && atPath(stack, want):
				if err := each(fields, order); err != nil {
					return err
				}
				fields = nil
			}
			stack = stack[:len(stack)-1]
		}
	}
}

// atPath reports whether the elements open, from the root, are those of
// path under it.
func atPath(stack, path []string) bool {
	if len(stack) != len(path)+1 {
		return false
	}
	for i, p := range path {
		if stack[i+1] != p {
			return false
		}
	}
	return true
}

// sniffTypes gives the text columns of src the narrowest type every
// value of theirs is written in: an integer, a number, a boolean, a date
// or a timestamp. A cast alone would round 9.5 to an integer, or cut a
// timestamp to its date, so each type has a pattern too. A value starting
// with a zero, as a postal code, keeps its column text, which a number
// would lose the zero of.
func (f *File) sniffTypes(ctx context.Context) error {
	candidates := []struct{ typ, is string }{
		{"BIGINT", `regexp_full_match(trim(%[1]s), '[+-]?[0-9]+') AND TRY_CAST(%[1]s AS BIGINT) IS NOT NULL`},
		{"DOUBLE", `TRY_CAST(%[1]s AS DOUBLE) IS NOT NULL`},
		{"BOOLEAN", `lower(trim(%[1]s)) IN ('true', 'false')`},
		{"DATE", `regexp_full_match(trim(%[1]s), '[0-9]{4}-[0-9]{2}-[0-9]{2}') AND TRY_CAST(%[1]s AS DATE) IS NOT NULL`},
		{"TIMESTAMP", `TRY_CAST(%[1]s AS TIMESTAMP) IS NOT NULL`},
	}
	var sel []string
	for _, c := range f.Columns {
		col := quoteIdent(c.Name)
		sel = append(sel, "count("+col+")", "count(*) FILTER (WHERE regexp_matches("+col+", '^[+-]?0[0-9]'))")
		for _, cand := range candidates {
			sel = append(sel, "count(*) FILTER (WHERE "+fmt.Sprintf(cand.is, col)+")")
		}
	}
	counts := make([]int64, len(sel))
	ptrs := make([]any, len(sel))
	for i := range counts {
		ptrs[i] = &counts[i]
	}
	if err := f.db.QueryRowContext(ctx, "SELECT "+strings.Join(sel, ", ")+" FROM src").Scan(ptrs...); err != nil {
		return err
	}
	per := 2 + len(candidates)
	var casts []string
	for i, c := range f.Columns {
		nonNull, zeroLed := counts[i*per], counts[i*per+1]
		if nonNull > 0 && zeroLed == 0 {
			for j, cand := range candidates {
				if counts[i*per+2+j] == nonNull {
					f.Columns[i].Type = cand.typ
					break
				}
			}
		}
		casts = append(casts, "CAST("+quoteIdent(c.Name)+" AS "+f.Columns[i].Type+") AS "+quoteIdent(c.Name))
	}
	for _, q := range []string{
		"CREATE TABLE typed AS SELECT " + strings.Join(casts, ", ") + " FROM src",
		"DROP TABLE src",
		"ALTER TABLE typed RENAME TO src",
	} {
		if _, err := f.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
