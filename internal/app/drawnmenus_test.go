package app

import (
	"testing"
	"time"

	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// A click on an item of the sidebar's menu, drawn by the app, does what
// the item says: the choice reaches the menu's function in the frame of
// the click, as one of the system's menu would.
func TestSidebarMenuItemClicked(t *testing.T) {
	a := newBareApp(t)
	p, _ := a.addProject(newProjectDir(t, "acme"))
	tt := ui.NewTester(a.view, 1200, 760)
	a.openSampleIn(p)
	testutil.WaitFor(t, tt, "the sample", func() bool { return a.tabs.Len() == 1 })
	tt.Advance(time.Second)
	tt.Frame()
	if err := tt.RightClick("customers"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("View DDL") {
		t.Fatalf("no menu: %q", tt.Texts())
	}
	before := a.tabs.Len()
	if err := tt.Click("View DDL"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.tabs.Len() != before+1 || tt.HasText("Copy to Another Database…") {
		t.Fatalf("View DDL: tabs %d -> %d, the menu still shown %v", before, a.tabs.Len(), tt.HasText("Copy to Another Database…"))
	}
}
