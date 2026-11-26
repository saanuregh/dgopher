package db

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Scan is a step of a plan that reads a table from storage.
type Scan struct {
	Schema, Table string
	Step          string  // the plan's name for it, as Seq Scan
	Rows          float64 // rows it reads; -1 until TableRowsSQL tells
}

// LargeScans runs the engine's scan check of stmt on sess and returns the
// scans over limit, largest first; a scan whose rows nothing tells is
// left out. ran reports each statement sent, for the audit log. ok false:
// the engine estimates none, and nothing is sent. A transaction open on
// sess is kept: the reads sit behind a savepoint where the engine has
// them (ReadAside). args are the values of stmt's placeholders, sent with
// the statement that makes the plan.
func LargeScans(ctx context.Context, sess *Session, stmt string, limit float64,
	ran func(sql string, took time.Duration, err error), args ...any) (scans []Scan, ok bool, err error) {
	r, ok := SQL(sess.db.Config.Engine)
	if !ok {
		return nil, false, nil
	}
	run, ok := r.ScanCheck(stmt)
	if !ok {
		return nil, false, nil
	}
	var all []Scan
	err = sess.ReadAside(ctx, func(ctx context.Context) error {
		var names [][]string
		var rows [][][]any
		err := sess.RunExplain(ctx, run, ran, func(ctx context.Context) error {
			var err error
			names, rows, err = readScanCheck(ctx, sess, run, ran, args)
			return err
		})
		if err != nil {
			return err
		}
		all = r.ScansOf(names, rows)
		return readTableRows(ctx, sess, r, all, ran)
	})
	if err != nil {
		return nil, true, err
	}
	for _, s := range all {
		if s.Rows > limit {
			scans = append(scans, s)
		}
	}
	slices.SortStableFunc(scans, func(a, b Scan) int { return cmp.Compare(b.Rows, a.Rows) })
	return scans, true, nil
}

// readScanCheck runs a scan check's statement with args, and its Read
// when it has one, and reads every result of the plan whole.
func readScanCheck(ctx context.Context, sess *Session, run ExplainRun,
	ran func(sql string, took time.Duration, err error), args []any) (names [][]string, rows [][][]any, err error) {
	sql := run.Statement
	if run.Read != "" {
		start := time.Now()
		if run.RunsWhole {
			var c *Cursor
			if c, err = sess.Query(ctx, sql, args...); err == nil {
				_, err = c.ReadAll(0)
				c.Close()
			}
		} else {
			_, err = sess.Exec(ctx, sql, args...)
		}
		ran(sql, time.Since(start), err)
		if err != nil {
			return nil, nil, err
		}
		sql, args = run.Read, nil
	}
	start := time.Now()
	names, rows, err = queryResults(ctx, sess, sql, args...)
	ran(sql, time.Since(start), err)
	return names, rows, err
}

// queryResults runs a query and reads each of its results whole, by its
// columns' names and rows.
func queryResults(ctx context.Context, sess *Session, sql string, args ...any) (names [][]string, rows [][][]any, err error) {
	c, err := sess.Query(ctx, sql, args...)
	if err != nil {
		return nil, nil, err
	}
	defer c.Close()
	following, err := c.ReadAll(MaxRows)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range append([]*Cursor{c}, following...) {
		got, err := r.Fetch(MaxRows)
		if err != nil {
			return nil, nil, err
		}
		cols := make([]string, len(r.Columns))
		for i, col := range r.Columns {
			cols[i] = col.Name
		}
		names, rows = append(names, cols), append(rows, got)
	}
	return names, rows, nil
}

// QueryValue runs a query and reads the first value of its first row,
// as a count's.
func QueryValue(ctx context.Context, sess *Session, sql string, args ...any) (any, error) {
	c, err := sess.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	rows, err := c.Fetch(1)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 || len(rows[0]) == 0 {
		return nil, errors.New("the query returned no row")
	}
	return rows[0][0], nil
}

// readTableRows fills in the rows of the scans whose plan does not tell
// them, from what the engine's TableRowsSQL reads; a negative count, as
// of a table never analyzed, leaves its scans at -1.
func readTableRows(ctx context.Context, sess *Session, r SQLRules, scans []Scan,
	ran func(sql string, took time.Duration, err error)) error {
	sql, args := r.TableRowsSQL(scans)
	if sql == "" {
		return nil
	}
	start := time.Now()
	_, results, err := queryResults(ctx, sess, sql, args...)
	ran(sql, time.Since(start), err)
	if err != nil {
		return err
	}
	for _, res := range results {
		for _, row := range res {
			if len(row) < 3 {
				continue
			}
			n := valueNumber(row[2])
			for i := range scans {
				if scans[i].Rows < 0 && scans[i].Schema == Display(row[0]) && scans[i].Table == Display(row[1]) && n >= 0 {
					scans[i].Rows = n
				}
			}
		}
	}
	return nil
}

// unknownRowScans are the tables, by schema and table, of the scans whose
// rows are not known, each once, for a TableRowsSQL.
func unknownRowScans(scans []Scan) [][2]string {
	var out [][2]string
	for _, s := range scans {
		if key := [2]string{s.Schema, s.Table}; s.Rows < 0 && s.Table != "" && !slices.Contains(out, key) {
			out = append(out, key)
		}
	}
	return out
}

// planScans lists the steps that pick takes as scans of the plan among
// an explain's results (PlanResults), in the plan's order.
func planScans(e Engine, names [][]string, rows [][][]any, pick func(n *PlanNode) (Scan, bool)) []Scan {
	plan, _ := PlanResults(e, names, rows)
	if plan == nil || plan.Root == nil {
		return nil
	}
	var out []Scan
	plan.Root.Walk(func(n *PlanNode, _ int) {
		if s, ok := pick(n); ok {
			out = append(out, s)
		}
	})
	return out
}

// targetTable is the table a step's Target names, without the index it
// reads it by.
func targetTable(n *PlanNode) string {
	table, _, _ := strings.Cut(n.Target, " using ")
	return table
}

// valueNumber is a value read from a server as a number, -1 when it is
// none.
func valueNumber(v any) float64 {
	if v == nil {
		return -1
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(Display(v)), 64)
	if err != nil {
		return -1
	}
	return f
}
