package export

import (
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duckdb/duckdb-go/v2"
)

// No format writes over a file that is there already: the export is
// refused, and the file keeps what it held.
func TestFileFormatsRefuseExistingFile(t *testing.T) {
	for _, f := range Formats() {
		path := filepath.Join(t.TempDir(), "out."+f.Extension())
		if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		w, err := NewFileWriter(path, f, []Column{{Name: "a", DatabaseType: "INTEGER"}}, Options{})
		if err == nil {
			w.Write([]any{int64(1)})
			err = w.Close()
		}
		if err == nil {
			t.Errorf("%s: an existing file was written over", f)
		}
		if got, _ := os.ReadFile(path); string(got) != "keep" {
			t.Errorf("%s: the existing file now holds %q", f, got)
		}
	}
}

// A value needing more decimals than its DECIMAL column holds fails,
// rather than being rounded; one that fits stays exact.
func TestDecimalRefusesRounding(t *testing.T) {
	for _, c := range []struct {
		v    any
		want string // the unscaled digits, "" when it must fail
	}{
		{"1.234", ""},
		{"1.23", "123"},
		{"1.2", "120"},
		{1.005, ""},
		{0.1, "10"},
		{big.NewInt(12), "1200"},
		{"-0.5", "-50"},
		{"123456789.1", ""}, // too wide for DECIMAL(10,2)
	} {
		got, ok := toDecimal(c.v, 10, 2)
		if c.want == "" {
			if ok {
				t.Errorf("%v: %v accepted", c.v, got)
			}
			continue
		}
		if !ok {
			t.Errorf("%v: refused", c.v)
			continue
		}
		if s := got.(duckdb.Decimal).Value.String(); s != c.want {
			t.Errorf("%v: %s, want %s", c.v, s, c.want)
		}
	}
}

// A column's type follows its first value that is not NULL, not the
// first row's; a HUGEINT column stays HUGEINT.
func TestDuckTypeFromFirstNonNullValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.duckdb")
	huge, _ := new(big.Int).SetString("123456789012345678901234567890", 10)
	cols := []Column{{"n", "integer"}, {"h", "hugeint"}}
	writeFile(t, path, DuckDBFile, cols, [][]any{{nil, nil}, {"abc", huge}, {int64(5), big.NewInt(3)}}, Options{})
	types, rows := readBack(t, path, `SELECT * FROM "export"`)
	if strings.Join(types, ",") != "VARCHAR,HUGEINT" {
		t.Errorf("types %v", types)
	}
	if len(rows) != 3 || rows[1][0] != "abc" || rows[2][0] != "5" || rows[1][1].(*big.Int).Cmp(huge) != 0 {
		t.Errorf("rows %#v", rows)
	}
}
