package app

import (
	"slices"
	"strings"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

func showsText(tt *ui.Tester, want string) bool {
	return slices.ContainsFunc(tt.Texts(), func(s string) bool { return strings.Contains(s, want) })
}

// Dialogs that write or restore name the connection and environment they
// act on, in the band the confirm dialog has.
func TestWritingDialogsShowEnvBand(t *testing.T) {
	const band = "lite · Production"
	a, tt, cn, _ := liteApp(t, db.Production)
	if _, err := cn.DB().SQL.Exec(`CREATE TABLE items (id integer primary key, name text)`); err != nil {
		t.Fatal(err)
	}
	closeAll := func() {
		a.sqlFile, a.backup, a.importing, a.copying, a.filling, a.confirm = nil, nil, nil, nil, nil, nil
	}

	a.startSQLFileRun(cn, "", writeSQL(t, "w.sql", "INSERT INTO items VALUES (1, 'a');\n"), "")
	testutil.WaitFor(t, tt, "the Run SQL File band", func() bool { return showsText(tt, band) })
	testutil.Snapshot(t, tt, "run-sql-file-env-band")
	closeAll()

	a.openBackup(cn, "", true)
	testutil.WaitFor(t, tt, "the Restore band", func() bool { return showsText(tt, band) })
	closeAll()

	a.openImportOf(cn, "", "main", nil, writeSQL(t, "rows.csv", "id,name\n1,a\n"))
	testutil.WaitFor(t, tt, "the Import band", func() bool { return showsText(tt, band) })
	closeAll()

	a.openFill(cn, "", db.Object{Schema: "main", Name: "items", Kind: db.KindTable})
	testutil.WaitFor(t, tt, "the Generate Test Data band", func() bool { return showsText(tt, band) })
	closeAll()

	dev := addConn(a, db.Config{ID: "dev", Name: "dev", Engine: db.SQLite, Database: writeSQL(t, "dev.sqlite", "")})
	connectNow(t, a, tt, dev)
	a.openCopy(dev, "", "main", nil)
	tt.Frame()
	tt.Frame()
	if showsText(tt, "· Production") || showsText(tt, "dev · ") {
		t.Fatalf("a band before a target is chosen: %q", tt.Texts())
	}
	a.copying.target.label = targetLabel(cn)
	testutil.WaitFor(t, tt, "the Copy Tables target band", func() bool { return showsText(tt, band) })
	testutil.Snapshot(t, tt, "copy-tables-env-band")
	closeAll()
}
