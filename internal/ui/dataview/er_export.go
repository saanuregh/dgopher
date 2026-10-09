package dataview

import (
	"bytes"
	"fmt"
	"html"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// The forms a diagram is saved in.
const (
	exportPNG = "png"
	exportSVG = "svg"
)

const (
	// erExportScale is the device pixels a PNG of a diagram draws per
	// point: sharp on a high-density screen.
	erExportScale = 2
	// erExportMax is the most pixels a PNG of a diagram is on a side, a
	// large schema's at a lesser scale.
	erExportMax = 16384
)

// save asks where the diagram goes, then writes it in the form given.
func (e *ERTab) save(form string) {
	name := strings.NewReplacer("/", "_", " ", "_").Replace(e.schema) + "-diagram." + form
	if e.focus != nil {
		name = e.focus.Name + "-diagram." + form
	}
	snapshot := e.snapshot()
	go func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: "Save the Diagram", DefaultPath: name})
		if err != nil || path == "" {
			return
		}
		started := time.Now()
		var data []byte
		if form == exportSVG {
			data = []byte(snapshot.svg())
		} else {
			data, err = snapshot.png()
		}
		if err == nil {
			err = os.WriteFile(path, data, 0o644)
		}
		e.a.Post(func() {
			if err != nil {
				e.a.ShowError("The diagram was not saved", err.Error())
				return
			}
			e.a.Notify(started, "Diagram saved", filepath.Base(path), nil)
			e.a.Toast("Saved the diagram to "+filepath.Base(path), "", nil)
		})
	}()
}

// snapshot is a copy of the diagram as it stands, which a save reads off
// the UI thread while the tables may move on it.
func (e *ERTab) snapshot() *ERTab {
	c := &ERTab{a: e.a, conn: e.conn, database: e.database, schema: e.schema, focus: e.focus, byName: map[string]*erTable{}}
	for _, t := range e.tables {
		copied := *t
		c.tables = append(c.tables, &copied)
		c.byName[erKey(t.schema, t.obj.Name)] = &copied
	}
	return c
}

// png draws the diagram as the app does, at erExportScale, in a light
// theme.
func (e *ERTab) png() ([]byte, error) {
	w, h := e.extent()
	scale := min(erExportScale, erExportMax/max(w, h))
	img := ui.Render(func(c *ui.Context) {
		ui.Box(c).Fill().Background(ui.Hex("#ffffff")).Children(func() { e.canvas(c, e.a) })
	}, int(w), int(h), scale)
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// The colors of a diagram saved as SVG, as the light theme draws it.
const (
	svgInk    = "#1f2328"
	svgMuted  = "#6e7781"
	svgBorder = "#d0d7de"
	svgHeader = "#f6f8fa"
	svgFocus  = "#ddeafd"
	svgLink   = "#2f6fed"
	svgKey    = "#d97706"
)

// svg writes the diagram as SVG, its tables and links where they stand.
func (e *ERTab) svg() string {
	w, h := e.extent()
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%g" height="%g" viewBox="0 0 %g %g" font-family="system-ui, -apple-system, 'Segoe UI', sans-serif">`+"\n", w, h, w, h)
	fmt.Fprintf(&b, `<rect width="100%%" height="100%%" fill="#ffffff"/>`+"\n")
	// The links under the tables, as on screen.
	for _, t := range e.tables {
		for _, fk := range t.fks {
			ref := e.refOf(t, fk)
			if ref == nil || len(fk.Columns) == 0 {
				continue
			}
			refCol := ""
			if len(fk.RefColumns) > 0 {
				refCol = fk.RefColumns[0]
			}
			y0, y1 := t.rowY(fk.Columns[0]), ref.rowY(refCol)
			if ref == t {
				x := t.x + erBoxW
				fmt.Fprintf(&b, `<polyline points="%g,%g %g,%g %g,%g %g,%g" fill="none" stroke="%s" stroke-width="1.5"/>`+"\n", x, y0, x+18, y0, x+18, y1, x, y1, svgLink)
				continue
			}
			x0, x1 := t.x, ref.x+erBoxW
			if ref.x > t.x {
				x0, x1 = t.x+erBoxW, ref.x
			}
			mid := (x0 + x1) / 2
			dir := float32(1)
			if x1 < mid {
				dir = -1
			}
			fmt.Fprintf(&b, `<polyline points="%g,%g %g,%g %g,%g %g,%g" fill="none" stroke="%s" stroke-width="1.5"/>`+"\n", x0, y0, mid, y0, mid, y1, x1, y1, svgLink)
			fmt.Fprintf(&b, `<line x1="%g" y1="%g" x2="%g" y2="%g" stroke="%s" stroke-width="1.5"/>`+"\n", x1-dir*6, y1-5, x1-dir*6, y1+5, svgLink)
			fmt.Fprintf(&b, `<circle cx="%g" cy="%g" r="3" fill="%s"/>`+"\n", x0, y0, svgLink)
		}
	}
	for _, t := range e.tables {
		e.svgTable(&b, t)
	}
	b.WriteString("</svg>\n")
	return b.String()
}

// svgTable writes a table's box: its name, then its columns with their
// types, keys marked, a column that takes no NULL in bold.
func (e *ERTab) svgTable(b *strings.Builder, t *erTable) {
	x, y, h := t.x, t.y, t.height()
	esc := html.EscapeString
	header := svgHeader
	if e.focus != nil && t.schema == e.focus.Schema && t.obj.Name == e.focus.Name {
		header = svgFocus
	}
	name := t.obj.Name
	if t.schema != e.schema {
		name = t.schema + "." + name
	}
	fmt.Fprintf(b, `<g><rect x="%g" y="%g" width="%d" height="%g" rx="8" fill="#ffffff" stroke="%s"/>`+"\n", x, y, erBoxW, h, svgBorder)
	fmt.Fprintf(b, `<path d="M%g,%g h%d a8,8 0 0 1 8,8 v%d h%d v%d a8,8 0 0 1 8,-8 z" fill="%s"/>`+"\n", x+8, y, erBoxW-16, erHeaderH-8, -erBoxW, -(erHeaderH - 8), header)
	fmt.Fprintf(b, `<line x1="%g" y1="%g" x2="%g" y2="%g" stroke="%s"/>`+"\n", x, y+erHeaderH, x+erBoxW, y+erHeaderH, svgBorder)
	fmt.Fprintf(b, `<text x="%g" y="%g" font-size="12.5" font-weight="700" fill="%s">%s</text>`+"\n", x+10, y+erHeaderH/2+4.5, svgInk, esc(fitText(name, 30)))
	for i, col := range t.cols {
		ry := y + erHeaderH + 4 + float32(i)*erRowH + erRowH/2 + 4
		if i == erMaxCols {
			fmt.Fprintf(b, `<text x="%g" y="%g" font-size="11" fill="%s">+ %d more</text>`+"\n", x+10, ry, svgMuted, len(t.cols)-erMaxCols)
			break
		}
		switch {
		case col.PrimaryKey:
			fmt.Fprintf(b, `<text x="%g" y="%g" font-size="9" font-weight="700" fill="%s">PK</text>`+"\n", x+8, ry, svgKey)
		case isFKColumn(t, col.Name):
			fmt.Fprintf(b, `<text x="%g" y="%g" font-size="9" font-weight="700" fill="%s">FK</text>`+"\n", x+8, ry, svgLink)
		}
		weight := "400"
		if !col.Nullable {
			weight = "600"
		}
		fmt.Fprintf(b, `<text x="%g" y="%g" font-size="12" font-weight="%s" fill="%s">%s</text>`+"\n", x+28, ry, weight, svgInk, esc(fitText(col.Name, 20)))
		fmt.Fprintf(b, `<text x="%g" y="%g" font-size="10.5" text-anchor="end" fill="%s">%s</text>`+"\n", x+erBoxW-10, ry, svgMuted, esc(fitText(col.Type, 16)))
	}
	b.WriteString("</g>\n")
}

// fitText cuts a text to n characters, an ellipsis ending it.
func fitText(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
