package sqltext

import "testing"

func TestChangeCaseSkipsStrings(t *testing.T) {
	src := `select name, 'mixed Text' from "Quoted" -- a comment` + "\nwhere x = $tag$keep me$tag$"
	got := ChangeCase(src, 0, len([]rune(src)), Postgres, true)
	want := `SELECT NAME, 'mixed Text' FROM "Quoted" -- a comment` + "\nWHERE X = $tag$keep me$tag$"
	if got != want {
		t.Fatalf("upper:\n%s\nwant\n%s", got, want)
	}
	if got := ChangeCase("SELECT A FROM B", 7, 8, Generic, false); got != "SELECT a FROM B" {
		t.Fatalf("range: %q", got)
	}
}

func TestToggleComment(t *testing.T) {
	src := "select 1\n  from t\n\nwhere x"
	got, _, _ := ToggleComment(src, 0, 15)
	if got != "-- select 1\n  -- from t\n\nwhere x" {
		t.Fatalf("comment: %q", got)
	}
	back, _, _ := ToggleComment(got, 0, 20)
	if back != src {
		t.Fatalf("uncomment: %q", back)
	}
}
