package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// CompareTargetLimit is how many rows of the target a comparison of
	// rows holds, to find each of the source's among them.
	CompareTargetLimit = 500_000
	// DifferenceLimit is how many differences a comparison keeps: past
	// it, it counts them only.
	DifferenceLimit = 100_000
)

// CompareTable is a table whose rows are compared.
type CompareTable struct {
	DB     *DB
	Schema string
	Table  string
}

// RowDifference is a row the two tables do not hold alike.
type RowDifference struct {
	// Kind is ChangeInsert for a row only in the source, ChangeDelete for
	// one only in the target, ChangeUpdate for one in both, changed.
	Kind ChangeKind
	// Source and Target are the row's values, by the comparison's
	// columns, as each table holds them; nil where it has no such row.
	Source, Target []any
	// Changed are the columns, by index, whose values differ.
	Changed []int
}

// RowComparison is what comparing the rows of a table with a target's
// found, matching them by the target's primary key, over the columns both
// have.
type RowComparison struct {
	Columns []string // as the source names them
	// TargetColumns are Columns as the target names them.
	TargetColumns []string
	// Key are the columns of the target's primary key, by index in
	// Columns, in the key's order.
	Key []int
	// Target is the target table, to change as the source is.
	Target EditTarget

	Same, OnlyInSource, OnlyInTarget, Changed int64
	// Differences are the first DifferenceLimit, in the order found.
	Differences []RowDifference

	sourceTypes []string // the canonical types of Columns in the source
}

// Complete reports whether every difference was kept.
func (r *RowComparison) Complete() bool {
	return int64(len(r.Differences)) == r.OnlyInSource+r.OnlyInTarget+r.Changed
}

// CompareRows compares the rows of source with target's. The target's
// rows are held, up to CompareTargetLimit; the source's are read as they
// come. progress tells the rows read so far, of both.
func CompareRows(ctx context.Context, source, target CompareTable, progress func(int64)) (*RowComparison, error) {
	srcCols, err := source.DB.Dialect.Columns(ctx, source.DB.Catalog(), source.Schema, source.Table)
	if err != nil {
		return nil, err
	}
	tgtCols, err := target.DB.Dialect.Columns(ctx, target.DB.Catalog(), target.Schema, target.Table)
	if err != nil {
		return nil, err
	}
	key, err := KeyColumns(tgtCols)
	if err != nil {
		return nil, fmt.Errorf("%s has no primary key to match its rows by", target.Table)
	}
	r := &RowComparison{Target: EditTarget{Dialect: target.DB.Dialect, Schema: target.Schema, Table: target.Table, Columns: tgtCols, Key: key}}
	// Each side's values are read as their type says, or, where it says
	// text only, as the other side's: PostgreSQL's numeric without its
	// sizes still compares as a number with MySQL's decimal(10,2).
	var sourceAs, targetAs []string
	taken := make([]bool, len(tgtCols))
	for _, sc := range srcCols {
		j := slices.IndexFunc(tgtCols, func(tc Column) bool { return tc.Name == sc.Name })
		if j < 0 {
			j = slices.IndexFunc(tgtCols, func(tc Column) bool { return strings.EqualFold(tc.Name, sc.Name) })
		}
		if j < 0 || taken[j] {
			continue
		}
		taken[j] = true
		tc := tgtCols[j]
		st, tt := CanonicalType(source.DB.Config.Engine, sc.Type), CanonicalType(target.DB.Config.Engine, tc.Type)
		r.Columns = append(r.Columns, sc.Name)
		r.TargetColumns = append(r.TargetColumns, tc.Name)
		r.sourceTypes = append(r.sourceTypes, st)
		if st == "VARCHAR" {
			st = tt
		} else if tt == "VARCHAR" {
			tt = st
		}
		sourceAs, targetAs = append(sourceAs, st), append(targetAs, tt)
	}
	for _, k := range key {
		i := slices.Index(r.TargetColumns, k)
		if i < 0 {
			return nil, fmt.Errorf("%s has no column %s, of %s's primary key, to match the rows by", source.Table, k, target.Table)
		}
		r.Key = append(r.Key, i)
	}

	var read int64
	tick := func(n int) {
		read += int64(n)
		progress(read)
	}
	// The target is read first, its session closed before the source's
	// opens: a database of one connection serves one at a time.
	held := map[string]int{}
	var rows [][]any
	err = eachRow(ctx, target, r.TargetColumns, func(batch [][]any) error {
		if len(rows)+len(batch) > CompareTargetLimit {
			return fmt.Errorf("%s has more than %d rows, more than are compared here", target.Table, CompareTargetLimit)
		}
		for _, row := range batch {
			k, err := r.keyText(row, targetAs, target.Table)
			if err != nil {
				return err
			}
			held[k] = len(rows)
			rows = append(rows, row)
		}
		tick(len(batch))
		return nil
	})
	if err != nil {
		return nil, err
	}
	seen := make([]bool, len(rows))
	err = eachRow(ctx, source, r.Columns, func(batch [][]any) error {
		for _, row := range batch {
			k, err := r.keyText(row, sourceAs, source.Table)
			if err != nil {
				return err
			}
			i, ok := held[k]
			if ok && seen[i] {
				return fmt.Errorf("%s holds the key %s more than once: its rows cannot be matched one to one", source.Table, r.keyLabel(row))
			}
			if !ok {
				r.OnlyInSource++
				r.keep(RowDifference{Kind: ChangeInsert, Source: row})
				continue
			}
			seen[i] = true
			var changed []int
			for c := range r.Columns {
				if !sameValue(row[c], sourceAs[c], rows[i][c], targetAs[c]) {
					changed = append(changed, c)
				}
			}
			if changed == nil {
				r.Same++
				continue
			}
			r.Changed++
			r.keep(RowDifference{Kind: ChangeUpdate, Source: row, Target: rows[i], Changed: changed})
		}
		tick(len(batch))
		return nil
	})
	if err != nil {
		return nil, err
	}
	for i, row := range rows {
		if !seen[i] {
			r.OnlyInTarget++
			r.keep(RowDifference{Kind: ChangeDelete, Target: row})
		}
	}
	return r, nil
}

func (r *RowComparison) keep(d RowDifference) {
	if len(r.Differences) < DifferenceLimit {
		r.Differences = append(r.Differences, d)
	}
}

// keyText is a row's key as one string, alike for the same key read from
// either table; a key with a NULL in it matches no row.
func (r *RowComparison) keyText(row []any, types []string, table string) (string, error) {
	var b strings.Builder
	for _, i := range r.Key {
		text, ok := comparableText(row[i], types[i])
		if !ok {
			return "", fmt.Errorf("%s has a row whose %s is NULL, which matches no row", table, r.Columns[i])
		}
		b.WriteString(strconv.Itoa(len(text)))
		b.WriteByte(':')
		b.WriteString(text)
	}
	return b.String(), nil
}

// keyLabel writes a row's key as the user reads it.
func (r *RowComparison) keyLabel(row []any) string {
	parts := make([]string, len(r.Key))
	for i, c := range r.Key {
		parts[i] = r.Columns[c] + " = " + Display(row[c])
	}
	return strings.Join(parts, ", ")
}

// eachRow reads the named columns of a table's rows, in batches, on a
// session of their own.
func eachRow(ctx context.Context, t CompareTable, columns []string, batch func([][]any) error) error {
	sess, err := t.DB.Session(ctx)
	if err != nil {
		return err
	}
	defer sess.Close()
	names := make([]string, len(columns))
	for i, c := range columns {
		names[i] = t.DB.Dialect.Quote(c)
	}
	cursor, err := sess.Query(ctx, "SELECT "+strings.Join(names, ", ")+" FROM "+QualifiedName(t.DB.Dialect, t.Schema, t.Table))
	if err != nil {
		return err
	}
	defer cursor.Close()
	for {
		rows, err := cursor.Fetch(1000)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			if cursor.Truncated() {
				return fmt.Errorf("%s keeps one connection, whose rows are read ahead up to %d: compare in parts", t.Table, MaxRows)
			}
			return nil
		}
		if err := batch(rows); err != nil {
			return err
		}
	}
}

// Changes are what make the target's rows as the source's: adding the
// rows only in the source, updating the changed, deleting the rows only
// in the target, as asked. The rows past DifferenceLimit are not among
// them.
func (r *RowComparison) Changes(add, update, remove bool) []Change {
	engine := r.Target.Dialect.Engine()
	key := func(row []any) []any {
		k := make([]any, len(r.Key))
		for i, c := range r.Key {
			k[i] = row[c]
		}
		return k
	}
	var out []Change
	for _, d := range r.Differences {
		switch {
		case d.Kind == ChangeInsert && add:
			values := map[string]any{}
			for c, name := range r.TargetColumns {
				values[name] = InsertValue(engine, d.Source[c], r.sourceTypes[c])
			}
			out = append(out, Change{Kind: ChangeInsert, Values: values})
		case d.Kind == ChangeUpdate && update:
			values := map[string]any{}
			for _, c := range d.Changed {
				values[r.TargetColumns[c]] = InsertValue(engine, d.Source[c], r.sourceTypes[c])
			}
			out = append(out, Change{Kind: ChangeUpdate, Key: key(d.Target), Values: values})
		case d.Kind == ChangeDelete && remove:
			out = append(out, Change{Kind: ChangeDelete, Key: key(d.Target)})
		}
	}
	return out
}

// sameValue reports whether two values, of columns of those canonical
// types, are the same value.
func sameValue(a any, aType string, b any, bType string) bool {
	at, aOK := comparableText(a, aType)
	bt, bOK := comparableText(b, bType)
	return aOK == bOK && at == bt
}

// timeLayouts are the forms a time is read from as text, as MySQL's text
// protocol sends it; time.Parse takes a fraction of a second after the
// seconds without one in the layout.
var timeLayouts = []string{
	"2006-01-02 15:04:05Z07:00", "2006-01-02T15:04:05Z07:00", "2006-01-02 15:04:05-07",
	"2006-01-02 15:04:05", "2006-01-02T15:04:05", time.DateOnly, time.TimeOnly,
}

// comparableText is a value as a comparison of rows reads it, alike for
// the same value whichever engine gave it: a number by its value, a
// boolean as true or false, a time in one form (in UTC where it has a
// zone), JSON compact with its keys sorted, the rest as its text. ok is
// false for NULL.
func comparableText(v any, canonical string) (text string, ok bool) {
	switch x := v.(type) {
	case nil:
		return "", false
	case time.Time:
		return comparableTime(x, canonical), true
	case []byte:
		text = string(x)
	default:
		text, _ = ValueText(v, canonical).(string)
	}
	base, _, _ := strings.Cut(canonical, "(")
	switch base {
	case "BOOLEAN":
		if b, ok := booleanText(text); ok {
			return b, true
		}
	case "BIGINT", "UBIGINT", "DECIMAL", "FLOAT", "DOUBLE":
		if n, ok := new(big.Rat).SetString(strings.TrimSpace(text)); ok {
			return n.RatString(), true
		}
	case "DATE", "TIME", "TIMESTAMP", "TIMESTAMPTZ":
		for _, layout := range timeLayouts {
			if t, err := time.Parse(layout, text); err == nil {
				return comparableTime(t, canonical), true
			}
		}
	case "JSON":
		dec := json.NewDecoder(strings.NewReader(text))
		dec.UseNumber()
		var doc any
		if dec.Decode(&doc) == nil {
			if out, err := json.Marshal(doc); err == nil {
				return string(out), true
			}
		}
	case "UUID":
		return strings.ToLower(text), true
	}
	return text, true
}

func comparableTime(t time.Time, canonical string) string {
	switch canonical {
	case "DATE":
		return t.Format(time.DateOnly)
	case "TIME":
		return t.Format("15:04:05.999999999")
	case "TIMESTAMPTZ":
		t = t.UTC()
	}
	return t.Format("2006-01-02 15:04:05.999999999")
}

// ApplyEdits runs statements changing rows in one transaction of the
// session's own, each changing as many rows as it wants, or none of them
// stays; each is told of every statement, by its index, as it ran.
func (s *Session) ApplyEdits(ctx context.Context, stmts []Statement, each func(i int, rows int64, took time.Duration, err error)) (err error) {
	if s.Tx() != TxNone {
		return errors.New("a transaction is open on this database: commit or roll it back first")
	}
	if err := s.Begin(ctx); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			// Even when ctx has ended, which stopped the statements.
			rollback, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if rerr := s.Rollback(rollback); rerr != nil && !errors.Is(rerr, ErrTxLost) {
				err = fmt.Errorf("%w\nThe rollback failed too: %v", err, rerr)
			}
		}
	}()
	for i, st := range stmts {
		start := time.Now()
		var n int64
		n, err = s.Exec(ctx, st.SQL, st.Args...)
		if err == nil && st.Want >= 0 && n >= 0 && n != st.Want {
			// Not the statement itself: its values may be hidden ones.
			err = fmt.Errorf("statement %d of %d changed %d rows instead of %d", i+1, len(stmts), n, st.Want)
		}
		each(i, n, time.Since(start), err)
		if err != nil {
			return err
		}
	}
	return s.Commit(ctx)
}
