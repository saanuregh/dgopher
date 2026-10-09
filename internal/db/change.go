package db

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// SchemaChange is the statements changing a table, and how they run.
type SchemaChange struct {
	Steps []Step
	// Atomic runs the steps in one transaction: all of them or none.
	Atomic bool
}

// Step is a statement of a change, or a SQLite table made again.
type Step struct {
	SQL string
	// Rebuild, when set, makes a SQLite table again, as SQLite changes
	// what ALTER TABLE cannot.
	Rebuild *Rebuild
}

// Rebuild makes a SQLite table again, as SQLite's documentation says:
// Create makes the new table under the name Temp, and Copy copies the
// rows into it; the old table is dropped, the new one takes its name,
// and the table's indexes and triggers, read before, are created again.
type Rebuild struct {
	Schema, Table, Temp string
	Create, Copy        string
}

func (r *Rebuild) drop() string {
	return "DROP TABLE " + QualifiedName(sqliteDialect{}, r.Schema, r.Table)
}

func (r *Rebuild) rename() string {
	return "ALTER TABLE " + QualifiedName(sqliteDialect{}, r.Schema, r.Temp) + " RENAME TO " + quoteDouble(r.Table)
}

// Text is the change as it is shown before it runs.
func (ch SchemaChange) Text() string {
	var parts []string
	for _, st := range ch.Steps {
		if st.Rebuild == nil {
			parts = append(parts, st.SQL+";")
			continue
		}
		r := st.Rebuild
		parts = append(parts, "-- SQLite cannot make this change in place: "+r.Table+" is made again.")
		for _, s := range []string{r.Create, r.Copy, r.drop(), r.rename()} {
			parts = append(parts, s+";")
		}
		parts = append(parts, "-- Then its indexes and triggers are created again.")
	}
	return strings.Join(parts, "\n")
}

// rebuilds reports whether the change makes a table again.
func (ch SchemaChange) rebuilds() bool {
	for _, st := range ch.Steps {
		if st.Rebuild != nil {
			return true
		}
	}
	return false
}

// Apply runs a change, telling each of every statement it runs, as it
// ran. An atomic change is rolled back when a step fails. A rebuild runs
// with SQLite's foreign key checks off, as the copy of the rows needs,
// and checks the keys before committing.
func (s *Session) Apply(ctx context.Context, ch SchemaChange, each func(stmt string, rows int64, took time.Duration, err error)) (err error) {
	exec := func(ctx context.Context, stmt string) error {
		start := time.Now()
		n, err := s.Exec(ctx, stmt)
		each(stmt, n, time.Since(start), err)
		return err
	}
	if ch.rebuilds() {
		if err := exec(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
			return err
		}
		defer func() {
			// Even when ctx has ended: the connection goes back to the pool.
			restore, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if rerr := exec(restore, "PRAGMA foreign_keys = ON"); err == nil {
				err = rerr
			}
		}()
	}
	if ch.Atomic {
		if err := exec(ctx, beginStatement(s.db.Config.Engine)); err != nil {
			return err
		}
		defer func() {
			if err != nil {
				rollback, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				exec(rollback, "ROLLBACK")
			}
		}()
	}
	for _, st := range ch.Steps {
		if st.Rebuild != nil {
			err = s.rebuild(ctx, st.Rebuild, exec)
		} else {
			err = exec(ctx, st.SQL)
		}
		if err != nil {
			return err
		}
	}
	if ch.rebuilds() {
		if err := s.checkForeignKeys(ctx); err != nil {
			return err
		}
	}
	if ch.Atomic {
		return exec(ctx, "COMMIT")
	}
	return nil
}

func beginStatement(e Engine) string {
	if e == MySQL {
		return "START TRANSACTION"
	}
	return "BEGIN"
}

// rebuild makes a SQLite table again, its indexes and triggers too.
func (s *Session) rebuild(ctx context.Context, r *Rebuild, exec func(context.Context, string) error) (err error) {
	dependents, err := s.queryStrings(ctx, `SELECT sql FROM `+quoteDouble(r.Schema)+`.sqlite_master
WHERE tbl_name = ? AND type IN ('index', 'trigger') AND sql IS NOT NULL ORDER BY type = 'trigger', name`, r.Table)
	if err != nil {
		return err
	}
	for _, stmt := range []string{r.Create, r.Copy, r.drop()} {
		if err := exec(ctx, stmt); err != nil {
			return err
		}
	}
	// The views naming the table name it again once the new table takes
	// its name: the rename must leave them as they are, not check them
	// against a schema without the table.
	if err := exec(ctx, "PRAGMA legacy_alter_table = ON"); err != nil {
		return err
	}
	err = exec(ctx, r.rename())
	if lerr := exec(context.WithoutCancel(ctx), "PRAGMA legacy_alter_table = OFF"); err == nil {
		err = lerr
	}
	if err != nil {
		return err
	}
	for _, stmt := range dependents {
		if err := exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// checkForeignKeys fails when a row's foreign key points at no row, as a
// rebuild with the checks off may leave.
func (s *Session) checkForeignKeys(ctx context.Context) error {
	broken, err := s.queryStrings(ctx, `SELECT "table" || ' row ' || COALESCE(rowid, '?') || ' points at no row of ' || parent FROM pragma_foreign_key_check LIMIT 3`)
	if err != nil {
		return err
	}
	if len(broken) > 0 {
		return fmt.Errorf("the change breaks foreign keys: %s", strings.Join(broken, "; "))
	}
	return nil
}

// queryStrings reads the first column of a statement's rows as text.
func (s *Session) queryStrings(ctx context.Context, query string, args ...any) ([]string, error) {
	c, err := s.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	var out []string
	for {
		rows, err := c.Fetch(1000)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return out, nil
		}
		for _, r := range rows {
			out = append(out, Display(r[0]))
		}
	}
}
