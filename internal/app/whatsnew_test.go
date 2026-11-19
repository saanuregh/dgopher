package app

import (
	"os"
	"path/filepath"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/settings"
	"dgopher/internal/store"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

const testChangelog = "# Changelog\n\n## 0.8.0\n\n- Connection folders arrived.\n\n## 0.7.3\n\n- Older notes.\n"

// useRelease makes the app's version and changelog v and text for the test.
func useRelease(t *testing.T, v, text string) {
	t.Helper()
	oldVersion, oldText := appVersion, Changelog
	appVersion, Changelog = func() string { return v }, text
	t.Cleanup(func() { appVersion, Changelog = oldVersion, oldText })
}

// storeWithSettings is a store whose settings.json holds s, as an earlier
// run left it.
func storeWithSettings(t *testing.T, s settings.Settings) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir(), store.MemorySecrets())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveJSON("settings.json", s); err != nil {
		t.Fatal(err)
	}
	return st
}

func lastSeen(t *testing.T, st *store.Store) string {
	t.Helper()
	var s settings.Settings
	if err := st.LoadJSON("settings.json", &s); err != nil {
		t.Fatal(err)
	}
	return s.LastSeenVersion
}

// The first start after an upgrade shows the new version's notes, once.
func TestWhatsNewShownOnceAfterUpgrade(t *testing.T) {
	useRelease(t, "0.8.0", testChangelog)
	prev := settings.Default()
	prev.TourDone, prev.LastSeenVersion = true, "0.7.3"
	st := storeWithSettings(t, prev)
	a := startApp(t, st)
	tt := ui.NewTester(a.view, 1280, 800)
	testutil.WaitFor(t, tt, "the release notes", func() bool { return tt.HasText("What's New in DGopher 0.8.0") })
	if !tt.HasText("Connection folders arrived.") || tt.HasText("Older notes.") {
		t.Fatalf("the dialog shows otherwise than 0.8.0's notes: %q", tt.Texts())
	}
	if got := lastSeen(t, st); got != "0.8.0" {
		t.Fatalf("last seen version %q, want 0.8.0", got)
	}
	if err := tt.Click("Close"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.whatsNewOpen || tt.HasText("What's New in DGopher 0.8.0") {
		t.Fatal("Close left the release notes open")
	}

	again := startApp(t, st)
	if again.whatsNewOpen {
		t.Fatal("a second start showed the release notes again")
	}
}

// A first install records its version without showing notes.
func TestWhatsNewSilentOnFirstInstall(t *testing.T) {
	useRelease(t, "0.8.0", testChangelog)
	st, err := store.Open(t.TempDir(), store.MemorySecrets())
	if err != nil {
		t.Fatal(err)
	}
	a := startApp(t, st)
	if a.whatsNewOpen {
		t.Fatal("a first install showed release notes")
	}
	if got := lastSeen(t, st); got != "0.8.0" {
		t.Fatalf("last seen version %q, want 0.8.0", got)
	}
}

// Going back to an older version shows nothing and records it.
func TestWhatsNewSilentOnDowngrade(t *testing.T) {
	useRelease(t, "0.7.3", testChangelog)
	prev := settings.Default()
	prev.TourDone, prev.LastSeenVersion = true, "0.8.0"
	st := storeWithSettings(t, prev)
	a := startApp(t, st)
	if a.whatsNewOpen {
		t.Fatal("a downgrade showed release notes")
	}
	if got := lastSeen(t, st); got != "0.7.3" {
		t.Fatalf("last seen version %q, want 0.7.3", got)
	}
}

// Help's What's New shows the current version's notes any time.
func TestWhatsNewFromHelpMenu(t *testing.T) {
	useRelease(t, "0.8.0", testChangelog)
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1280, 800)
	tt.Frame()
	if tt.HasText("What's New in DGopher 0.8.0") {
		t.Fatal("the notes showed before they were asked for")
	}
	for _, g := range a.appMenus() {
		for _, e := range g.items {
			if e.label == "What's New in DGopher" {
				e.run()
			}
		}
	}
	testutil.WaitFor(t, tt, "the release notes", func() bool { return tt.HasText("Connection folders arrived.") })
	testutil.Snapshot(t, tt, "whats-new")
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.whatsNewOpen {
		t.Fatal("Esc left the release notes open")
	}
}

// The tour's banner stands in for the notes only when the start page,
// where it shows, is what the launch shows: with tabs restored, a user
// who never took the tour sees the notes, once.
func TestWhatsNewShownWithoutTourWhenTabsRestored(t *testing.T) {
	useRelease(t, "0.7.3", testChangelog)
	prev := settings.Default()
	prev.TourDone, prev.LastSeenVersion = false, "0.7.3"
	st := storeWithSettings(t, prev)
	a := startApp(t, st)
	projDir := filepath.Join(t.TempDir(), "proj")
	os.MkdirAll(projDir, 0o755)
	p, err := a.addProject(projDir)
	if err != nil {
		t.Fatal(err)
	}
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	a.saveProject(p)
	tt := ui.NewTester(a.view, 900, 600)
	newAppEditor(t, a, tt, cn, "SELECT 1;")
	a.saveWorkspace(true)

	useRelease(t, "0.8.0", testChangelog)
	b := startApp(t, st)
	if b.tabs().Len() != 1 {
		t.Fatalf("restored %d tabs", b.tabs().Len())
	}
	if !b.whatsNewOpen {
		t.Fatal("the release notes did not show behind a restored tab")
	}
	if again := startApp(t, st); again.whatsNewOpen {
		t.Fatal("a second start showed the release notes again")
	}
}

// Without tabs to restore, the start page's tour banner comes first: no
// notes for a user who never took the tour.
func TestWhatsNewSilentBehindTourBanner(t *testing.T) {
	useRelease(t, "0.8.0", testChangelog)
	prev := settings.Default()
	prev.TourDone, prev.LastSeenVersion = false, "0.7.3"
	st := storeWithSettings(t, prev)
	if a := startApp(t, st); a.whatsNewOpen {
		t.Fatal("the notes showed over the tour banner")
	}
	if got := lastSeen(t, st); got != "0.8.0" {
		t.Fatalf("last seen version %q, want 0.8.0", got)
	}
}
