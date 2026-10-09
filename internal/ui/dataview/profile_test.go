package dataview

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

func TestProfileRows(t *testing.T) {
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	src := &Source{Cols: []db.ColumnInfo{{Name: "n", Type: "int"}, {Name: "s", Type: "text"}, {Name: "t", Type: "timestamp"}}}
	for i := range 100 {
		var s any = "b"
		if i%4 == 0 {
			s = "a"
		}
		if i%10 == 0 {
			s = nil
		}
		src.Rows = append(src.Rows, []any{int64(i), s, day.Add(time.Duration(i) * time.Hour)})
	}
	ps := profileRows(src, func(v any) string { return db.Display(v) })
	n, s, tm := ps[0], ps[1], ps[2]
	if n.min != "0" || n.max != "99" || n.avg != "49.5" || n.median != "49.5" || n.distinct != 100 || len(n.bins) != profileBins || n.bins[0] != 5 {
		t.Errorf("numbers %+v", n)
	}
	if s.nulls != 10 || s.distinct != 2 || len(s.top) != 2 || s.top[0] != (topValue{"b", 70}) || s.bins != nil {
		t.Errorf("text %+v", s)
	}
	if tm.min != db.Display(day) || len(tm.bins) != profileBins {
		t.Errorf("times %+v", tm)
	}
}

// Every row is profiled on the server: by SUMMARIZE on DuckDB, by one
// statement of counts and bounds elsewhere, which leaves out what the
// server cannot compare.
func TestProfileOnServer(t *testing.T) {
	for _, engine := range []db.Engine{db.DuckDB, db.SQLite} {
		t.Run(string(engine), func(t *testing.T) {
			a := NewFakeHost(t)
			cn := a.AddConn(db.Config{ID: "x", Name: "x", Engine: engine, Database: ":memory:"})
			tt := ui.NewTester(a.View, 1100, 760)
			a.Connect(cn, nil)
			testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
			ctx := context.Background()
			for _, q := range []string{"CREATE TABLE p (id INTEGER PRIMARY KEY, price DOUBLE, note TEXT)",
				"INSERT INTO p SELECT i, i * 1.5, CASE WHEN i % 2 = 0 THEN 'even' END FROM (WITH RECURSIVE r(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM r WHERE i < 1000) SELECT i FROM r)"} {
				if _, err := cn.DB.SQL.ExecContext(ctx, q); err != nil {
					t.Fatal(err)
				}
			}
			a.OpenTable(cn, "", db.Object{Schema: "main", Name: "p", Kind: db.KindTable, Rows: -1}, PageData)
			tb := a.Tabs[0].(*TableTab)
			testutil.WaitFor(t, tt, "rows", func() bool { return len(tb.view.src.Rows) > 0 })
			g := tb.view.grid
			g.ShowValue, g.panel = true, slices.Index(panelNames, "Profile")
			tt.Frame()
			if err := tt.Click("Profile Every Row"); err != nil {
				t.Fatal(err)
			}
			testutil.WaitFor(t, tt, "the profile", func() bool { return g.profileServer != nil || g.profileErr != "" })
			if g.profileErr != "" {
				t.Fatal(g.profileErr)
			}
			byName := map[string]columnProfile{}
			for _, p := range g.profileServer {
				byName[p.name] = p
			}
			id, note := byName["id"], byName["note"]
			if id.count != 1000 || id.min != "1" || !strings.HasPrefix(id.max, "1") || note.nulls != 500 {
				t.Fatalf("profiles %+v", g.profileServer)
			}
			testutil.Snapshot(t, tt, fmt.Sprint("profile-", engine))
		})
	}
}
