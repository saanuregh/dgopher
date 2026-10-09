package testdata

import (
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSuggest(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		col  Column
		want Kind
	}{
		{Column{Name: "id", Canonical: "BIGINT", AutoIncrement: true, Unique: true}, Default},
		{Column{Name: "id", Canonical: "BIGINT", Unique: true}, Sequence},
		{Column{Name: "code", Type: "varchar(8)", Canonical: "VARCHAR", Unique: true}, Words},
		{Column{Name: "code", Type: "text", Canonical: "VARCHAR", Unique: true}, UUID},
		{Column{Name: "customer_id", Canonical: "BIGINT", References: true}, Reference},
		{Column{Name: "status", Canonical: "VARCHAR", Enum: []string{"new", "done"}}, OneOf},
		{Column{Name: "email", Canonical: "VARCHAR"}, Email},
		{Column{Name: "firstName", Canonical: "VARCHAR"}, FirstName},
		{Column{Name: "surname", Canonical: "VARCHAR"}, LastName},
		{Column{Name: "contact_name", Canonical: "VARCHAR"}, FullName},
		{Column{Name: "phone_number", Canonical: "VARCHAR"}, Phone},
		{Column{Name: "website", Canonical: "VARCHAR"}, URL},
		{Column{Name: "notes", Type: "varchar(100)", Canonical: "VARCHAR"}, Sentence},
		{Column{Name: "description", Canonical: "VARCHAR"}, Paragraph},
		{Column{Name: "name", Canonical: "VARCHAR"}, Words},
		{Column{Name: "active", Canonical: "BOOLEAN"}, Boolean},
		{Column{Name: "price", Canonical: "DECIMAL(5,2)"}, Decimal},
		{Column{Name: "born_on", Canonical: "DATE"}, Date},
		{Column{Name: "created_at", Canonical: "TIMESTAMPTZ"}, Timestamp},
		{Column{Name: "doc", Canonical: "JSON"}, JSON},
		{Column{Name: "price", Type: "numeric", Canonical: "VARCHAR"}, Decimal},
		{Column{Name: "id", Type: "numeric(12,0)", Canonical: "DECIMAL(12,0)", Unique: true}, Sequence},
		{Column{Name: "span", Type: "interval", Canonical: "VARCHAR", Nullable: true}, Null},
		{Column{Name: "addr", Type: "inet", Canonical: "VARCHAR"}, Default},
		{Column{Name: "tags", Type: "text[]", Canonical: "VARCHAR", Nullable: true}, Null},
		{Column{Name: "title", Type: "Nullable(String)", Canonical: "VARCHAR"}, Words},
		{Column{Name: "title", Type: "FixedString(8)", Canonical: "VARCHAR"}, Words},
		{Column{Name: "region_id", Canonical: "BIGINT", PartOfForeignKey: true, References: true}, Default},
	} {
		if got := Suggest(c.col, now); got.Kind != c.want {
			t.Errorf("%s (%s): %s, want %s", c.col.Name, c.col.Canonical, got.Kind.Label(), c.want.Label())
		}
	}
	if g := Suggest(Column{Name: "price", Canonical: "DECIMAL(5,2)"}, now); g.To != "999" || g.Scale != 2 {
		t.Errorf("decimal(5,2): to %s, scale %d", g.To, g.Scale)
	}
	for typ, to := range map[string]string{"tinyint": "100", "Nullable(Int8)": "100", "UInt8": "100", "int8": "1000", "year": "2026"} {
		if g := Suggest(Column{Name: "level", Type: typ, Canonical: "BIGINT"}, now); g.To != to {
			t.Errorf("%s: to %s, want %s", typ, g.To, to)
		}
	}
	if g := Suggest(Column{Name: "born", Type: "Date", Canonical: "DATE"}, now); g.From != "1970-01-01" {
		t.Errorf("ClickHouse's Date: from %s", g.From)
	}
	if g := Suggest(Column{Name: "birthday", Canonical: "DATE"}, now); g.To != "2008-05-01" {
		t.Errorf("birthday: to %s", g.To)
	}
}

func TestValues(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	values := func(g Generator, n int) []any {
		t.Helper()
		next, err := g.Prepare("ab")
		if err != nil {
			t.Fatalf("%s: %v", g.Kind.Label(), err)
		}
		out := []any{}
		for i := range n {
			out = append(out, next(r, i))
		}
		return out
	}
	for _, v := range values(Generator{Kind: Integer, From: "-5", To: "5"}, 200) {
		if n := v.(int64); n < -5 || n > 5 {
			t.Fatalf("integer %d out of range", n)
		}
	}
	for _, v := range values(Generator{Kind: Decimal, From: "1", To: "2", Scale: 3}, 50) {
		if s := v.(string); len(s) != 5 || s < "1.000" || s > "2.000" {
			t.Fatalf("decimal %q", s)
		}
	}
	if got := values(Generator{Kind: Sequence, From: "41"}, 3); got[0] != int64(41) || got[2] != int64(43) {
		t.Fatalf("sequence %v", got)
	}
	for _, v := range values(Generator{Kind: Date, From: "2024-02-01", To: "2024-02-03"}, 50) {
		if d := v.(time.Time); d.Before(time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)) || d.After(time.Date(2024, 2, 3, 0, 0, 0, 0, time.UTC)) || d.Hour() != 0 {
			t.Fatalf("date %v", d)
		}
	}
	seen := map[string]bool{}
	for _, v := range values(Generator{Kind: Email, Unique: true, MaxLength: 24}, 300) {
		s := v.(string)
		if seen[s] || len(s) > 24 || !strings.HasSuffix(s, "@example.com") || !strings.Contains(s, ".ab") {
			t.Fatalf("unique e-mail %q", s)
		}
		seen[s] = true
	}
	for i, v := range values(Generator{Kind: Words, Unique: true, MaxLength: 11}, 20) {
		if s := v.(string); len([]rune(s)) > 11 || i == 19 && !strings.HasSuffix(s, "-ab20") {
			t.Fatalf("unique words cut to 11: %q", s)
		}
	}
	for _, v := range values(Generator{Kind: Date, From: "1700-01-01", To: "2200-12-31"}, 50) {
		if y := v.(time.Time).Year(); y < 1700 || y > 2200 {
			t.Fatalf("a date of five centuries: %d", y)
		}
	}
	nulls := 0
	for _, v := range values(Generator{Kind: City, NullPercent: 50}, 1000) {
		if v == nil {
			nulls++
		}
	}
	if nulls < 400 || nulls > 600 {
		t.Fatalf("%d NULLs of 1000 at 50%%", nulls)
	}
	u := values(Generator{Kind: UUID}, 1)[0].(string)
	if len(u) != 36 || u[14] != '4' || !strings.ContainsRune("89ab", rune(u[19])) {
		t.Fatalf("uuid %s", u)
	}
	if _, err := strconv.Atoi(values(Generator{Kind: Words}, 1)[0].(string)); err == nil {
		t.Fatal("words are a number")
	}
}

func TestPrepareRefuses(t *testing.T) {
	for _, g := range []Generator{
		{Kind: Words, Unique: true, MaxLength: 9},
		{Kind: Email, Unique: true, MaxLength: 21},
		{Kind: Integer, From: "9", To: "1"},
		{Kind: Integer, From: "x"},
		{Kind: Integer, From: "-9223372036854775808", To: "9223372036854775807"},
		{Kind: Decimal, To: "inf"},
		{Kind: Date, From: "yesterday", To: "2024-01-01"},
		{Kind: Timestamp, From: "2024-02-01", To: "2024-01-01"},
		{Kind: OneOf},
		{Kind: Reference},
		{Kind: Default},
		{Kind: City, NullPercent: 101},
	} {
		if _, err := g.Prepare("x"); err == nil {
			t.Errorf("%s from %q to %q: accepted", g.Kind.Label(), g.From, g.To)
		}
	}
}
