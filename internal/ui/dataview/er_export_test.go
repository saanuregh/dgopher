package dataview

import (
	"bytes"
	"encoding/xml"
	"image/png"
	"strings"
	"testing"

	"dgopher/internal/db"
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
