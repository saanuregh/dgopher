package dataview

import (
	"bytes"
	"encoding/xml"
	"image/png"
	"path/filepath"
	"strings"
	"testing"

	"dgopher/internal/db"

	"github.com/egoist/mygo/ui"
)

// A diagram saves as SVG, its tables, columns and links in it, and as
// a PNG twice its size, as the screen draws it.
func TestERExport(t *testing.T) {
	a := NewFakeHost(t)
	e := &ERTab{a: a, conn: a.AddConn(db.Config{ID: "x", Name: "x", Engine: db.SQLite, Database: ":memory:"}), schema: "main"}
	customers := &erTable{schema: "main", obj: db.Object{Name: "customers"}, cols: []db.Column{{Name: "id", Type: "INTEGER", PrimaryKey: true}, {Name: "name & title", Type: "TEXT", Nullable: true}}}
	orders := &erTable{schema: "main", obj: db.Object{Name: "orders"}, cols: []db.Column{{Name: "id", Type: "INTEGER", PrimaryKey: true}, {Name: "customer_id", Type: "INTEGER"}},
		fks: []db.ForeignKey{{Columns: []string{"customer_id"}, RefTable: "customers", RefColumns: []string{"id"}}}}
	e.tables = []*erTable{customers, orders}
	e.byName = map[string]*erTable{erKey("main", "customers"): customers, erKey("main", "orders"): orders}
	e.layout()

	svg := e.snapshot().svg()
	if err := xml.Unmarshal([]byte(svg), new(struct{})); err != nil {
		t.Fatalf("not XML: %v\n%s", err, svg)
	}
	for _, want := range []string{">customers<", ">orders<", ">customer_id<", ">name &amp; title<", "<polyline", ">PK<", ">FK<"} {
		if !strings.Contains(svg, want) {
			t.Errorf("no %s in\n%s", want, svg)
		}
	}
	data, err := e.snapshot().png()
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	w, h := e.extent()
	if b := img.Bounds(); b.Dx() != int(w*erExportScale) || b.Dy() != int(h*erExportScale) {
		t.Fatalf("PNG of %v for a diagram of %v×%v", b, w, h)
	}
}

// A diagram that cannot be written says so in a notice of its tab, as a
// saved one does, beside the error shown.
func TestERSaveFailureNotifies(t *testing.T) {
	a := NewFakeHost(t)
	e := &ERTab{a: a, conn: a.AddConn(db.Config{ID: "x", Name: "x", Engine: db.SQLite, Database: ":memory:"}), schema: "main", byName: map[string]*erTable{}}
	tt := ui.NewTester(a.View, 400, 300)
	dir := t.TempDir()
	e.writeTo(e.snapshot(), exportSVG, filepath.Join(dir, "missing", "main-diagram.svg"))
	e.writeTo(e.snapshot(), exportSVG, filepath.Join(dir, "main-diagram.svg"))
	tt.Frame()
	if len(a.Reports) != 1 || len(a.Notified) != 2 {
		t.Fatalf("reports %q, notices %+v", a.Errors, a.Notified)
	}
	for i, want := range []string{"Diagram not saved", "Diagram saved"} {
		n := a.Notified[i]
		if n.Title != want || !strings.Contains(n.Body, "main-diagram.svg") || n.Tab != e || n.Started.IsZero() {
			t.Errorf("notice %d: %+v", i, n)
		}
	}
	if b := a.Notified[0].Body; !strings.Contains(b, "no such file") {
		t.Errorf("failure body %q", b)
	}
}
