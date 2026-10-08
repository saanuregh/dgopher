package params

import (
	"strings"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/sqltext"
)

func TestBind(t *testing.T) {
	auto := func(v string) Input { return Input{Value: v, Kind: "auto"} }
	values := map[string]Input{":id": auto("42"), ":name": auto("O'Hara"), "${tbl}": auto("people"), ":zip": auto("0123"), ":gone": auto("null")}
	sql, args, shown, err := Bind("SELECT * FROM ${tbl} WHERE id = :id AND name = :name AND zip = :zip AND x = :gone OR id = :id", db.MySQL, sqltext.MySQL, values)
	if err != nil || sql != "SELECT * FROM people WHERE id = ? AND name = ? AND zip = ? AND x = ? OR id = ?" {
		t.Fatalf("sql %q %v", sql, err)
	}
	want := []any{int64(42), "O'Hara", "0123", nil, int64(42)}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("arg %d: %#v, want %#v", i, args[i], want[i])
		}
	}
	if !strings.Contains(shown, ":name = 'O''Hara'") || !strings.Contains(shown, ":gone = NULL") {
		t.Fatalf("shown %q", shown)
	}
	// PostgreSQL gets text, which it reads as the type the statement needs:
	// a number sent to a text column fails there.
	_, args, _, _ = Bind("SELECT :id", db.Postgres, sqltext.Postgres, map[string]Input{":id": auto("12345")})
	if args[0] != "12345" {
		t.Fatalf("postgres %#v", args[0])
	}
	// Not numbers: NaN, Inf, an integer past int64.
	for _, v := range []string{"nan", "Inf", "18446744073709551615"} {
		if _, args, _, _ = Bind("SELECT :v", db.MySQL, sqltext.MySQL, map[string]Input{":v": auto(v)}); args[0] != v {
			t.Errorf("%s bound as %#v", v, args[0])
		}
	}
	// A chosen kind wins; a number that is not one says so.
	_, args, _, _ = Bind("SELECT :v", db.Postgres, sqltext.Postgres, map[string]Input{":v": {Value: "7", Kind: "number"}})
	if args[0] != int64(7) {
		t.Fatalf("number %#v", args[0])
	}
	if _, _, _, err := Bind("SELECT :v", db.Postgres, sqltext.Postgres, map[string]Input{":v": {Value: "seven", Kind: "number"}}); err == nil {
		t.Fatal("seven as a number")
	}
}

func TestBindMixedStyles(t *testing.T) {
	values := map[string]Input{":a": {Value: "1", Kind: "auto"}}
	for _, c := range []struct {
		sql     string
		e       db.Engine
		d       sqltext.Dialect
		wantErr bool
	}{
		{"select $1, :a", db.Postgres, sqltext.Postgres, true},
		{"select ?, :a", db.MySQL, sqltext.MySQL, true},
		{"select ?, :a", db.SQLite, sqltext.SQLite, true},
		{"select :a, x ? 'k'", db.Postgres, sqltext.Postgres, false},
		{"select :a", db.MySQL, sqltext.MySQL, false},
	} {
		_, _, _, err := Bind(c.sql, c.e, c.d, values)
		if (err != nil) != c.wantErr {
			t.Fatalf("%q: err %v, want error %v", c.sql, err, c.wantErr)
		}
	}
}

func TestBindServerParams(t *testing.T) {
	sql := "select {id:UInt32}, { id : UInt32 }"
	if keys := Keys([]string{sql}, sqltext.ClickHouse); len(keys) != 1 || keys[0] != "{id:UInt32}" {
		t.Fatalf("keys %q", keys)
	}
	for _, c := range []struct {
		in   Input
		want string
	}{
		{Input{Value: "5", Kind: "auto"}, "5"},
		{Input{Value: "null", Kind: "auto"}, `\N`},
		{Input{Value: "x", Kind: "null"}, `\N`},
		{Input{Value: "null", Kind: "text"}, "null"},
	} {
		got, args, shown, err := Bind(sql, db.ClickHouse, sqltext.ClickHouse, map[string]Input{"{id:UInt32}": c.in})
		if err != nil || got != sql {
			t.Fatalf("sql %q %v", got, err)
		}
		if len(args) != 2 || args[0] != (db.ServerParam{Name: "id", Value: c.want}) {
			t.Fatalf("%+v: args %#v", c.in, args)
		}
		if shown != "{id:UInt32} = "+c.want {
			t.Fatalf("shown %q", shown)
		}
	}
}

func TestKeysSQLiteSigils(t *testing.T) {
	keys := Keys([]string{"select @a, $b, :c"}, sqltext.SQLite)
	if strings.Join(keys, " ") != "@a $b :c" {
		t.Fatalf("keys %q", keys)
	}
}
