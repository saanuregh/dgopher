package dataview

import (
	"slices"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
)

func TestLooksSensitive(t *testing.T) {
	for name, want := range map[string]bool{
		"password": true, "password_hash": true, "apiKey": true, "APIKey": true, "access_token": true, "card_number": true,
		"pin": true, "user_pin": true, "ssn": true, "passenger": false, "shipping": false, "tokens_used": false, "spinner": false, "name": false,
	} {
		if got := looksSensitive(name); got != want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

// A sensitive column's values show masked, and a column hidden for the
// team is kept in the project's file; showing it takes it out.
func TestHiddenValues(t *testing.T) {
	a, tt, cn := designHost(t)
	cn.DB.SQL.Exec(`ALTER TABLE items ADD COLUMN api_token TEXT`)
	cn.DB.SQL.Exec(`UPDATE items SET api_token = 'tok-secret-1'`)
	a.OpenTable(cn, "", db.Object{Schema: "main", Name: "items", Kind: db.KindTable, Rows: -1}, PageData)
	v := a.Tabs[0].(*TableTab).view
	testutil.WaitFor(t, tt, "the rows", func() bool { return len(v.src.Rows) == 2 })
	if testutil.HasTextContaining(tt, "tok-secret") || !tt.HasText(maskedText) {
		t.Fatalf("the token shows: %q", tt.Texts())
	}
	label := slices.IndexFunc(v.src.Cols, func(c db.ColumnInfo) bool { return c.Name == "label" })
	v.setHiddenValues("label", true)
	tt.Frame()
	if !v.grid.isMasked(label) || !slices.Contains(cn.Project.HiddenValues[v.historyKey()], "label") || tt.HasText("pen") {
		t.Fatalf("label is not hidden: %v", cn.Project.HiddenValues)
	}
	v.setHiddenValues("label", false)
	v.chooseMask("api_token", false)
	tt.Frame()
	if v.grid.isMasked(label) || len(cn.Project.HiddenValues) != 0 || !tt.HasText("tok-secret-1") {
		t.Fatalf("shown again: %v %q", cn.Project.HiddenValues, tt.Texts())
	}
	a.Settings().ShowSensitive = true
	v.maskChosen = nil
	tt.Frame()
	if !tt.HasText("tok-secret-1") {
		t.Fatal("the setting does not show sensitive values")
	}
}
