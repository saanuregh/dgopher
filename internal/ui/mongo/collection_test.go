package mongo

import (
	"slices"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/ui/dataview"

	"github.com/egoist/mygo/ui"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// While the changes are being saved, Discard and Find are refused, from
// their keys, the find bar's Enter and the buttons alike, and say why:
// a save that fails part way applies what it saved to the documents it
// started from, so they must stay as they were. No server is needed: the
// tab is never connected.
func TestCollectionRefusesDiscardAndFindWhileSaving(t *testing.T) {
	h := testHost{dataview.NewFakeHost(t)}
	cfg := offlineConfig(t)
	cn := h.AddConn(cfg)
	ct := NewCollection(h, cn, "shop", "orders")
	h.AddTab(ct)
	tt := ui.NewTester(h.View, 1360, 820)

	ct.read = []bson.Raw{rawDoc(t, bson.D{{Key: "_id", Value: int32(1)}, {Key: "status", Value: "open"}})}
	ct.readVer++
	ct.edits.edit(ct.read, 0, rawDoc(t, bson.D{{Key: "_id", Value: int32(1)}, {Key: "status", Value: "paid"}}))
	ct.loadErr, ct.saving = "", true
	tt.Frame()

	refused := func(what string, do func()) {
		t.Helper()
		toasts := len(h.Toasts)
		do()
		tt.Frame()
		if ct.edits.count() != 1 {
			t.Errorf("%s dropped the changes being saved", what)
		}
		if ct.loadErr != "" || h.Pending != nil {
			t.Errorf("%s found the documents again: loadErr %q, asked %+v", what, ct.loadErr, h.Pending)
		}
		if len(h.Toasts) != toasts+1 || h.Toasts[len(h.Toasts)-1] != beingSaved {
			t.Errorf("%s did not say why: toasts %q", what, h.Toasts[toasts:])
		}
	}
	refused("Discard", ct.Discard)
	refused("Find", ct.Find)
	refused("the Discard key", func() { tt.Key(ui.Cmd, ui.KeyR) })
	refused("the Refresh key", func() { tt.Key(0, ui.KeyF5) })
	refused("Enter in the filter", func() {
		*h.FocusWant() = "filter"
		tt.Frame()
		if !tt.Focused("FILTER") {
			t.Fatal("the filter is not focused")
		}
		tt.Key(0, ui.KeyEnter)
	})
	for _, button := range []string{"Find", "Refresh", "Discard"} {
		toasts := len(h.Toasts)
		_ = tt.Click(button)
		tt.Frame()
		if ct.edits.count() != 1 || ct.loadErr != "" || h.Pending != nil || len(h.Toasts) != toasts {
			t.Errorf("the %s button acted while saving: %d pending, loadErr %q, toasts %q", button, ct.edits.count(), ct.loadErr, h.Toasts[toasts:])
		}
	}
	if !slices.Contains(tt.Texts(), ct.edits.summary()) {
		t.Errorf("the pending changes are not shown: %q", tt.Texts())
	}

	ct.saving = false
	ct.Discard()
	if ct.edits.count() != 0 {
		t.Error("Discard after the save did not drop the changes")
	}
}

// While the documents are read, they cannot be edited or saved: a find
// drops the edits when it ends. While the changes are saved, no more
// documents are read: a save that fails part way applies what it saved
// to the documents it started from.
func TestCollectionRefusesEditsWhileReadingAndReadsWhileSaving(t *testing.T) {
	h := testHost{dataview.NewFakeHost(t)}
	cn := h.AddConn(offlineConfig(t))
	ct := NewCollection(h, cn, "shop", "orders")
	h.AddTab(ct)
	tt := ui.NewTester(h.View, 1360, 820)
	ct.read = []bson.Raw{rawDoc(t, bson.D{{Key: "_id", Value: int32(1)}, {Key: "status", Value: "open"}})}
	ct.readVer++
	ct.edits.edit(ct.read, 0, rawDoc(t, bson.D{{Key: "_id", Value: int32(1)}, {Key: "status", Value: "paid"}}))
	ct.loadErr, ct.loading = "", true
	tt.Frame()

	ct.Save()
	if h.Confirm != nil || len(h.Errors) != 1 || h.Errors[0] != "Not saved: "+beingRead {
		t.Errorf("Save while reading: confirm %v, errors %q", h.Confirm, h.Errors)
	}
	ct.view.grid.SelRow = 0
	for what, edit := range map[string]func(){
		"delete": ct.deleteChosen,
		"insert": func() { ct.openInsert(bson.D{{Key: "n", Value: int32(1)}}); ct.keepInsert() },
		"field":  func() { ct.openFieldEdit(0, "status"); ct.field.text = `"x"`; ct.keepField(false) },
	} {
		toasts := len(h.Toasts)
		edit()
		if ct.edits.count() != 1 || len(h.Toasts) != toasts+1 || h.Toasts[toasts] != beingRead {
			t.Errorf("%s while reading: %d pending, toasts %q", what, ct.edits.count(), h.Toasts[toasts:])
		}
	}

	ct.loading, ct.saving, ct.cursor, ct.done = false, true, &db.DocCursor{}, false
	tt.Frame()
	for what, read := range map[string]func(){"Load More": ct.LoadMore, "Fetch All": ct.FetchAll} {
		toasts := len(h.Toasts)
		read()
		if ct.loading || len(h.Toasts) != toasts+1 || h.Toasts[toasts] != beingSaved {
			t.Errorf("%s while saving: loading %v, toasts %q", what, ct.loading, h.Toasts[toasts:])
		}
	}
	for _, button := range []string{"Load More", "Fetch All"} {
		_ = tt.Click(button)
		tt.Frame()
		if ct.loading {
			t.Errorf("the %s button read while saving", button)
		}
	}
	ct.saving, ct.cursor = false, nil
}

// offlineConfig is a MongoDB connection the test never connects.
func offlineConfig(t *testing.T) db.Config {
	t.Helper()
	cfg, err := db.ParseURL("mongodb://127.0.0.1:1/")
	if err != nil {
		t.Fatal(err)
	}
	cfg.ID, cfg.Name, cfg.Env = "mg", "catalog", db.Development
	return cfg
}
