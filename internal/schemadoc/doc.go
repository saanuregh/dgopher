package schemadoc

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"regexp"
	"slices"
	"strings"

	"dgopher/internal/db"
)

// section is a part of the documentation: the objects of a kind.
type section struct {
	Title   string
	Entries []entry
}

// entry is an object as the documentation shows it.
type entry struct {
	Anchor       string
	Name         string
	Kind         string
	Comment      string
	Facts        []string // as "about 1,204 rows" or "on orders"
	Columns      []columnRow
	Indexes      []string
	ForeignKeys  []string
	ReferencedBy []string
	Definition   string
	Err          string
}

type columnRow struct {
	Name, Type, Default, Key, Comment string
	Nullable                          bool
}

// sections arranges what was read: tables, then views, then items by
// kind, each in the order read.
func sections(s *Schema) []section {
	anchors := map[string]int{}
	anchor := func(kind, name string) string {
		a := slug(kind + "-" + name)
		anchors[a]++
		if n := anchors[a]; n > 1 {
			a += fmt.Sprintf("-%d", n-1)
		}
		return a
	}
	var tables, views section
	tables.Title, views.Title = "Tables", "Views"
	for _, t := range s.Tables {
		e := tableEntry(t)
		e.Anchor = anchor(e.Kind, e.Name)
		if t.Kind == db.KindTable || t.Kind == db.KindForeignTable {
			tables.Entries = append(tables.Entries, e)
		} else {
			views.Entries = append(views.Entries, e)
		}
	}
	out := []section{tables, views}
	// Partitions show under their tables in the navigator, here after
	// the other kinds.
	for _, kind := range append(db.ItemKinds(), db.ItemPartition) {
		sec := section{Title: kind.Plural()}
		for _, it := range s.Items {
			if it.Kind != kind {
				continue
			}
			e := entry{Name: it.Name, Kind: string(it.Kind), Definition: strings.TrimSpace(it.Definition), Err: it.Err}
			if kind == db.ItemFunction || kind == db.ItemProcedure {
				e.Name = it.Label() // with its arguments, which tell overloads apart
			}
			switch {
			case kind == db.ItemPartition:
				e.Facts = append(e.Facts, "of "+it.Table)
			case it.Table != "":
				e.Facts = append(e.Facts, "on "+it.Table)
			}
			if it.Detail != "" && kind != db.ItemFunction && kind != db.ItemProcedure {
				e.Facts = append(e.Facts, it.Detail)
			}
			e.Anchor = anchor(e.Kind, it.Name)
			sec.Entries = append(sec.Entries, e)
		}
		out = append(out, sec)
	}
	return slices.DeleteFunc(out, func(sec section) bool { return len(sec.Entries) == 0 })
}

func tableEntry(t Table) entry {
	e := entry{Name: t.Name, Kind: string(t.Kind), Comment: t.Comment, Definition: strings.TrimSpace(t.Definition), Err: t.Err}
	if t.Kind != db.KindTable {
		e.Facts = append(e.Facts, string(t.Kind))
	}
	if t.Rows >= 0 && t.Kind == db.KindTable {
		e.Facts = append(e.Facts, fmt.Sprintf("about %s rows", thousands(t.Rows)))
	}
	if t.Engine != "" {
		e.Facts = append(e.Facts, t.Engine)
	}
	if t.Partitioning != "" {
		e.Facts = append(e.Facts, "partitioned by "+t.Partitioning)
	}
	refs := map[string]string{}
	for _, fk := range t.ForeignKeys {
		target := qualified(t.Schema, fk.RefSchema, fk.RefTable)
		e.ForeignKeys = append(e.ForeignKeys, fmt.Sprintf("%s: (%s) → %s (%s)", fk.Name,
			strings.Join(fk.Columns, ", "), target, strings.Join(fk.RefColumns, ", ")))
		if len(fk.Columns) == 1 && len(fk.RefColumns) == 1 {
			refs[fk.Columns[0]] = target + "." + fk.RefColumns[0]
		}
	}
	for _, c := range t.Columns {
		var keys []string
		if c.PrimaryKey {
			keys = append(keys, "PK")
		}
		if r, ok := refs[c.Name]; ok {
			keys = append(keys, "→ "+r)
		}
		def := ""
		if c.HasDefault {
			def = c.Default
		}
		if c.AutoIncrement && def == "" {
			def = "auto increment"
		}
		e.Columns = append(e.Columns, columnRow{Name: c.Name, Type: c.Type, Nullable: c.Nullable, Default: def,
			Key: strings.Join(keys, " "), Comment: c.Comment})
	}
	for _, ix := range t.Indexes {
		what := "index"
		switch {
		case ix.Primary:
			what = "primary key"
		case ix.Unique:
			what = "unique index"
		}
		e.Indexes = append(e.Indexes, fmt.Sprintf("%s: %s on (%s)", ix.Name, what, strings.Join(ix.Columns, ", ")))
	}
	for _, r := range t.ReferencedBy {
		e.ReferencedBy = append(e.ReferencedBy, fmt.Sprintf("%s (%s) by %s", qualified(t.Schema, r.Schema, r.Table),
			strings.Join(r.Columns, ", "), r.Name))
	}
	return e
}

// qualified names a table, with its schema when that is not the one
// documented.
func qualified(documented, schema, table string) string {
	if schema == "" || schema == documented {
		return table
	}
	return schema + "." + table
}

func thousands(n int64) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

var notSlug = regexp.MustCompile(`[^\p{L}\p{N}_-]+`)

// slug is text as an HTML id and a Markdown heading's anchor: lower
// case, a dash between words.
func slug(text string) string {
	return strings.Trim(notSlug.ReplaceAllString(strings.ToLower(text), "-"), "-")
}

// Markdown writes documentation of the schema read, under a title.
func Markdown(s *Schema, title string) string {
	var b strings.Builder
	secs := sections(s)
	fmt.Fprintf(&b, "# %s\n\n", title)
	for _, sec := range secs {
		fmt.Fprintf(&b, "- **%s:** ", sec.Title)
		for i, e := range sec.Entries {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "[%s](#%s)", markdownText(e.Name), e.Anchor)
		}
		b.WriteString("\n")
	}
	for _, sec := range secs {
		fmt.Fprintf(&b, "\n## %s\n", sec.Title)
		for _, e := range sec.Entries {
			fmt.Fprintf(&b, "\n<a id=\"%s\"></a>\n\n### %s\n\n", e.Anchor, markdownText(e.Name))
			if len(e.Facts) > 0 {
				fmt.Fprintf(&b, "*%s*\n\n", markdownText(strings.Join(e.Facts, " · ")))
			}
			if e.Comment != "" {
				fmt.Fprintf(&b, "%s\n\n", markdownText(e.Comment))
			}
			if e.Err != "" {
				fmt.Fprintf(&b, "> Could not be read: %s\n\n", markdownText(e.Err))
			}
			if len(e.Columns) > 0 {
				b.WriteString("| Column | Type | Null | Default | Key | Comment |\n| --- | --- | --- | --- | --- | --- |\n")
				for _, c := range e.Columns {
					null := ""
					if c.Nullable {
						null = "yes"
					}
					fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", markdownText(c.Name), markdownCode(c.Type), null,
						markdownCode(c.Default), markdownText(c.Key), markdownText(c.Comment))
				}
				b.WriteString("\n")
			}
			for _, list := range []struct {
				title string
				items []string
			}{{"Indexes", e.Indexes}, {"Foreign keys", e.ForeignKeys}, {"Referenced by", e.ReferencedBy}} {
				if len(list.items) == 0 {
					continue
				}
				fmt.Fprintf(&b, "%s:\n\n", list.title)
				for _, it := range list.items {
					fmt.Fprintf(&b, "- %s\n", markdownText(it))
				}
				b.WriteString("\n")
			}
			if e.Definition != "" {
				fence := "```"
				for strings.Contains(e.Definition, fence) {
					fence += "`"
				}
				fmt.Fprintf(&b, "<details><summary>Definition</summary>\n\n%ssql\n%s\n%s\n\n</details>\n\n", fence, e.Definition, fence)
			}
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

var markdownSpecial = strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "`", "\\`", "[", `\[`, "]", `\]`, "<", "&lt;", ">", "&gt;", "|", `\|`, "#", `\#`)

// markdownText is text as Markdown shows it as it is.
func markdownText(s string) string {
	return markdownSpecial.Replace(strings.Join(strings.Fields(s), " "))
}

// markdownCode is text in a cell as code.
func markdownCode(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	tick := "`"
	for strings.Contains(s, tick) {
		tick += "`"
	}
	pad := ""
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		pad = " "
	}
	return tick + pad + strings.ReplaceAll(s, "|", `\|`) + pad + tick
}

//go:embed doc.html
var htmlSource string

var htmlTemplate = template.Must(template.New("doc").Parse(htmlSource))

// HTML writes documentation of the schema read as one page, under a
// title.
func HTML(s *Schema, title string) (string, error) {
	var b bytes.Buffer
	err := htmlTemplate.Execute(&b, struct {
		Title    string
		Sections []section
	}{title, sections(s)})
	return b.String(), err
}
