package fileimport

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strings"
)

// xmlMaxDepth is how deep an XML file's rows are looked for.
const xmlMaxDepth = 8

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
	var names []string
	index := map[string]int{}
	var rows []map[string][]string
	err := xmlRows(f.Path, f.Rows, func(fields map[string][]string, order []string) error {
		for _, n := range order {
			if _, ok := index[n]; !ok {
				index[n] = len(names)
				names = append(names, n)
			}
		}
		rows = append(rows, fields)
		return ctx.Err()
	})
	if err != nil {
		return err
	}
	if len(rows) == 0 {
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
		for _, fields := range rows {
			vals := make([]any, len(names))
			for n, vs := range fields {
				switch len(vs) {
				case 1:
					vals[index[n]] = vs[0]
				default:
					out, _ := json.Marshal(vs)
					vals[index[n]] = string(out)
				}
			}
			if err := add(vals); err != nil {
				return err
			}
		}
		return nil
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
	d := xml.NewDecoder(file)
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
	d := xml.NewDecoder(file)
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
