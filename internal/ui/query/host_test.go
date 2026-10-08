package query

import (
	"testing"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dataview"

	"github.com/egoist/mygo/ui"
)

// fakeQueryHost is the query tab's host in tests: the data view's fake,
// with the query tab's dialogs; snippets do nothing, saves are counted.
type fakeQueryHost struct {
	*dataview.FakeHost
	dialogs Dialogs
	saves   int
	// switched are the connections editors were asked to switch to.
	switched []string
	// afterRun, when set, runs after each statement recorded, on the
	// goroutine that ran it.
	afterRun func(kind string)
}

func (h *fakeQueryHost) RecordRun(cfg db.Config, kind, database, stmt string, rows int64, d time.Duration, err error) {
	h.FakeHost.RecordRun(cfg, kind, database, stmt, rows, d, err)
	if h.afterRun != nil {
		h.afterRun(kind)
	}
}

func newFakeQueryHost(t *testing.T) *fakeQueryHost {
	return &fakeQueryHost{FakeHost: dataview.NewFakeHost(t)}
}

func (h *fakeQueryHost) QueryDialogs() *Dialogs { return &h.dialogs }

func (h *fakeQueryHost) AskSnippet(*Tab) {}

func (h *fakeQueryHost) SaveSQLFile(*Tab, bool) { h.saves++ }

func (h *fakeQueryHost) ScanQueries(*project.Project, bool) {}

func (h *fakeQueryHost) SwitchConnection(q *Tab, id string) { h.switched = append(h.switched, id) }

// view draws the window, and the query tab's dialogs over it.
func (h *fakeQueryHost) view(c *ui.Context) {
	h.FakeHost.View(c)
	DialogsView(h, c)
}

// newEditor opens an editor on text, connected, as the active tab.
func newEditor(t *testing.T, h *fakeQueryHost, tt *ui.Tester, cn *connection.Conn, text string) *Tab {
	t.Helper()
	h.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	q := New(h, cn, "", "query", text)
	h.AddTab(q)
	tt.Frame()
	return q
}
