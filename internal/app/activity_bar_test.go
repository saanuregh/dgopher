package app

import (
	"testing"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/query"

	"github.com/egoist/mygo/ui"
)

// Work going on shows as the status bar's thin bar once it lasts a
// moment, without moving what the status bar shows, and goes once done.
func TestActivityBar(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	before, ok := tt.Find("No connection")
	if !ok {
		t.Fatalf("texts %q", tt.Texts())
	}
	release := make(chan struct{})
	a.Background(func() func() {
		<-release
		return nil
	})
	tt.Frame()
	if tt.HasText("Working") {
		t.Fatal("shown at once")
	}
	a.busySince = time.Now().Add(-time.Second)
	tt.Frame()
	if !tt.HasText("Working") {
		t.Fatalf("no bar while work goes on: %q", tt.Texts())
	}
	testutil.Snapshot(t, tt, "activity-bar")
	if at, _ := tt.Find("No connection"); at != before {
		t.Fatalf("the status bar moved: %v, was %v", at, before)
	}
	close(release)
	testutil.WaitFor(t, tt, "the work to end", func() bool { return !tt.HasText("Working") })
}

// A query run quickly leaves its tab as it was: work going on is the
// status bar's to show, not the tab's.
func TestQuickRunKeepsTab(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: writeSQL(t, "q.sqlite", "")})
	tt := ui.NewTester(a.view, 1200, 800)
	connectNow(t, a, tt, cn)
	q := newAppEditor(t, a, tt, cn, "WITH RECURSIVE g(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM g WHERE x < 300000) SELECT count(*) FROM g")
	tt.Frame()
	if q.CloseReason() != "" {
		t.Fatalf("a reason before the run: %q", q.CloseReason())
	}
	closeLabel := "Close " + q.Title()
	before, ok := tt.Find(closeLabel)
	if !ok {
		t.Fatalf("no %q: %q", closeLabel, tt.Texts())
	}
	q.Run(query.RunStatement)
	running := 0
	for f := 0; f < 40 && (f < 2 || q.Running); f++ {
		tt.Frame()
		if q.Running {
			running++
		}
		if at, _ := tt.Find(closeLabel); at != before {
			t.Fatalf("frame %d (running %v): the tab's close button at %v, was %v", f, q.Running, at, before)
		}
		if tt.HasText("A statement is still running. Closing the editor cancels it.") {
			t.Fatalf("frame %d: the tab marks the run", f)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if running == 0 {
		t.Fatal("no frame drew the run")
	}
}
