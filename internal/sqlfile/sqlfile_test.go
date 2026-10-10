package sqlfile

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"dgopher/internal/sqltext"
)

// all reads every statement, and the rows of each COPY.
func all(t *testing.T, src string, d sqltext.Dialect) ([]Statement, []string) {
	t.Helper()
	r := NewReader(strings.NewReader(src), d)
	var stmts []Statement
	var data []string
	for {
		st, err := r.Next()
		if errors.Is(err, io.EOF) {
			return stmts, data
		}
		if err != nil {
			t.Fatal(err)
		}
		stmts = append(stmts, st)
		if st.Copy {
			b, err := io.ReadAll(r.Data())
			if err != nil {
				t.Fatal(err)
			}
			data = append(data, string(b))
		}
	}
}

// A file many reads long gives every statement whole, across the reads:
// those of many lines, of many-byte characters, and one longer than a
// read.
func TestStreamsLongFiles(t *testing.T) {
	var b strings.Builder
	var want []string
	for i := range 20000 {
		s := fmt.Sprintf("INSERT INTO t VALUES (%d, 'ünïcødé ✓ %d',\n  'two lines')", i, i)
		if i == 7000 {
			s = "INSERT INTO t VALUES ('" + strings.Repeat("é", 400_000) + "')"
		}
		want = append(want, s)
		b.WriteString(s + ";\n")
	}
	got, _ := all(t, b.String(), sqltext.Postgres)
	if len(got) != len(want) {
		t.Fatalf("%d statements, want %d", len(got), len(want))
	}
	line := 1
	for i, st := range got {
		if st.SQL != want[i] || st.Line != line {
			t.Fatalf("statement %d at line %d: %.60q, want line %d", i, st.Line, st.SQL, line)
		}
		line += strings.Count(want[i], "\n") + 1
	}
}

// A dump of pg_dump: its \restrict lines are skipped, and each COPY gives
// its rows, which hold what would otherwise split statements.
func TestPostgresDump(t *testing.T) {
	src := `\restrict abc123

--
-- PostgreSQL database dump
--

SET statement_timeout = 0;

CREATE TABLE public.t (a integer, b text);

COPY public.t (a, b) FROM stdin;
1	semi; colon
2	\N
3	'quote
\.

SELECT pg_catalog.setval('public.t_seq', 3, true);

\unrestrict abc123
`
	got, data := all(t, src, sqltext.Postgres)
	var sqls []string
	for _, st := range got {
		sqls = append(sqls, fmt.Sprintf("%d:%s", st.Line, st.SQL))
	}
	want := []string{"7:SET statement_timeout = 0", "9:CREATE TABLE public.t (a integer, b text)",
		"11:COPY public.t (a, b) FROM stdin", "17:SELECT pg_catalog.setval('public.t_seq', 3, true)"}
	if fmt.Sprint(sqls) != fmt.Sprint(want) {
		t.Fatalf("statements\n%q\nwant\n%q", sqls, want)
	}
	if len(data) != 1 || data[0] != "1\tsemi; colon\n2\t\\N\n3\t'quote\n" {
		t.Fatalf("rows %q", data)
	}
	// Rows not read are skipped.
	r := NewReader(strings.NewReader(src), sqltext.Postgres)
	for i := 0; ; i++ {
		_, err := r.Next()
		if errors.Is(err, io.EOF) {
			if i != 4 {
				t.Fatalf("%d statements", i)
			}
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestPsqlCommandsStop(t *testing.T) {
	for _, src := range []string{"SELECT 1;\n\\connect other\nSELECT 2;\n", "-- note\n\\connect other\n", "/* note */ \\i other.sql\n"} {
		r := NewReader(strings.NewReader(src), sqltext.Postgres)
		var err error
		for err == nil {
			_, err = r.Next()
		}
		if errors.Is(err, io.EOF) || !strings.Contains(err.Error(), "command of psql") {
			t.Errorf("%q: %v", src, err)
		}
	}
}

// A dump of mysqldump keeps its triggers whole under DELIMITER, also when
// a read ends in the middle of one.
func TestMySQLDelimiter(t *testing.T) {
	var b strings.Builder
	n := 0
	for i := range 3000 {
		fmt.Fprintf(&b, "INSERT INTO t VALUES (%d);\n", i)
		n++
		if i%500 == 0 {
			fmt.Fprintf(&b, "DELIMITER ;;\n/*!50003 CREATE*/ /*!50003 TRIGGER t%d BEFORE INSERT ON t FOR EACH ROW BEGIN SET NEW.a = NEW.a + 1; SET NEW.b = 2; END */;;\nDELIMITER ;\n", i)
			n++
		}
	}
	got, _ := all(t, b.String(), sqltext.MySQL)
	if len(got) != n {
		t.Fatalf("%d statements, want %d", len(got), n)
	}
	for _, st := range got {
		if strings.Contains(st.SQL, "TRIGGER") && !strings.HasSuffix(st.SQL, "END */") {
			t.Fatalf("a trigger split: %q", st.SQL)
		}
		if strings.Contains(st.SQL, "DELIMITER") {
			t.Fatalf("a directive given as a statement: %q", st.SQL)
		}
	}
}

// limit lowers a size limit for one test.
func limit(t *testing.T, v *int, n int) {
	old := *v
	*v = n
	t.Cleanup(func() { *v = old })
}

func TestStatementSizeCap(t *testing.T) {
	limit(t, &maxStatement, 1<<20)
	src := "SELECT 1;\n\nSELECT 'never closes\n" + strings.Repeat("x", 3<<20)
	r := NewReader(strings.NewReader(src), sqltext.Postgres)
	if _, err := r.Next(); err != nil {
		t.Fatal(err)
	}
	_, err := r.Next()
	if err == nil || !strings.Contains(err.Error(), "line 3:") || !strings.Contains(err.Error(), "never close") {
		t.Fatalf("got %v", err)
	}
	// A statement under the limit still reads whole.
	long := "SELECT '" + strings.Repeat("y", 900<<10) + "'"
	got, _ := all(t, long+";\nSELECT 2;\n", sqltext.Postgres)
	if len(got) != 2 || got[0].SQL != long {
		t.Fatalf("%d statements", len(got))
	}
}

// countingReader counts the reads made of it.
type countingReader struct {
	r     io.Reader
	reads int
}

func (c *countingReader) Read(p []byte) (int, error) {
	c.reads++
	return c.r.Read(p)
}

func TestCopyLineGrowth(t *testing.T) {
	line := strings.Repeat("z", 32<<20)
	src := "COPY t (a) FROM stdin;\n" + line + "\n\\.\n"
	c := &countingReader{r: strings.NewReader(src)}
	r := NewReader(c, sqltext.Postgres)
	if _, err := r.Next(); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(r.Data())
	if err != nil || len(b) != len(line)+1 {
		t.Fatalf("%d bytes: %v", len(b), err)
	}
	// Growing by a fixed chunk would take 128 reads.
	if c.reads > 40 {
		t.Fatalf("%d reads", c.reads)
	}
	limit(t, &maxLine, 1<<20)
	for _, src := range []string{"COPY t (a) FROM stdin;\n" + strings.Repeat("z", 3<<20), `\restrict ` + strings.Repeat("z", 3<<20)} {
		r = NewReader(strings.NewReader(src), sqltext.Postgres)
		_, err = r.Next()
		if err == nil {
			_, err = io.ReadAll(r.Data())
		}
		if err == nil || !strings.Contains(err.Error(), "line 1") && !strings.Contains(err.Error(), "line 2") {
			t.Fatalf("%.20q: %v", src, err)
		}
	}
}
