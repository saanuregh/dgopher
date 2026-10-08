package audit

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"time"
)

// Open opens the log kept in a state file.
func Open(db *sql.DB) (*Log, error) {
	return &Log{db: db, user: currentUser()}, nil
}

// Record appends an event in one transaction that reads the last entry
// first: windows sharing the file keep one chain.
func (l *Log) Record(e Event) error {
	if l == nil {
		return nil
	}
	tx, err := l.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	seq, prev, err := lastRow(tx)
	if err != nil {
		return err
	}
	if err := insert(tx, l.chain(e, seq, prev)); err != nil {
		return err
	}
	return tx.Commit()
}

// Head returns the last entry's number and hash: written down elsewhere,
// as in a ticket or a chat, they anchor the log, so that a later rewrite
// of it, whole or of its end, shows.
func (l *Log) Head() (int64, string) {
	seq, hash, _ := lastRow(l.db)
	return seq, hash
}

func currentUser() string {
	name := ""
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	if h, err := os.Hostname(); err == nil {
		name += "@" + h
	}
	return name
}

// chain numbers an event after the one of seq and hash prev, and seals it.
func (l *Log) chain(e Event, seq int64, prev string) Event {
	e.Seq, e.Prev = seq+1, prev
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	e.Time = e.Time.UTC().Truncate(time.Millisecond)
	e.User = l.user
	e.Hash = hashOf(e)
	return e
}

type querier interface {
	QueryRow(query string, args ...any) *sql.Row
}

func lastRow(q querier) (int64, string, error) {
	var seq int64
	var hash string
	err := q.QueryRow("SELECT seq, hash FROM audit ORDER BY id DESC LIMIT 1").Scan(&seq, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}
	return seq, hash, err
}

func insert(tx *sql.Tx, e Event) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO audit (seq, time, kind, hash, event) VALUES (?, ?, ?, ?, ?)",
		e.Seq, e.Time.UTC().Format(time.RFC3339Nano), e.Kind, e.Hash, string(data))
	return err
}

// Read returns the newest events first, at most limit (all for 0).
func (l *Log) Read(limit int) ([]Event, error) {
	if limit <= 0 {
		limit = -1
	}
	rows, err := l.db.Query("SELECT event FROM audit ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return out, err
		}
		var e Event
		if json.Unmarshal([]byte(data), &e) == nil {
			out = append(out, e)
		}
	}
	return out, rows.Err()
}

// Verify checks the whole log, in the order written: that the first entry
// is the first of all, that every entry's hash holds, and that each names
// the one before it. It finds an entry changed, removed, inserted or
// moved, and a log begun anew; not the newest entries cut off the end,
// nor a log rewritten whole: compare the head (Head) with a copy kept
// elsewhere for that.
func (l *Log) Verify() (Verification, error) {
	rows, err := l.db.Query("SELECT event FROM audit ORDER BY id")
	if err != nil {
		return Verification{}, err
	}
	defer rows.Close()
	var c chainCheck
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return c.v, err
		}
		if !c.next([]byte(data)) {
			return c.v, nil
		}
	}
	return c.v, rows.Err()
}

// chainCheck follows the chain one entry after the other.
type chainCheck struct {
	v       Verification
	prev    string
	prevSeq int64
}

func (c *chainCheck) next(data []byte) bool {
	var e Event
	broken := func(seq int64, reason string) bool {
		c.v.Broken, c.v.Reason = seq, reason
		return false
	}
	if err := json.Unmarshal(data, &e); err != nil {
		return broken(c.prevSeq+1, "the entry is not valid JSON")
	}
	switch {
	case hashOf(e) != e.Hash:
		return broken(e.Seq, "the entry was changed after it was written")
	case e.Prev != c.prev:
		if c.prevSeq == 0 {
			return broken(e.Seq, fmt.Sprintf("the log starts at entry %d: the entries before it were removed", e.Seq))
		}
		return broken(e.Seq, "an entry before this one was removed, changed or inserted")
	case e.Seq != c.prevSeq+1:
		return broken(e.Seq, fmt.Sprintf("entries %d to %d are missing", c.prevSeq+1, e.Seq-1))
	}
	c.prev, c.prevSeq = e.Hash, e.Seq
	c.v.Entries++
	return true
}

// VerifyFile checks a log exported to a file, from the first entry of all.
func VerifyFile(path string) (Verification, error) {
	f, err := os.Open(path)
	if err != nil {
		return Verification{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	var c chainCheck
	for sc.Scan() {
		if !c.next(sc.Bytes()) {
			return c.v, nil
		}
	}
	return c.v, sc.Err()
}

// Export writes the whole log, oldest entry first, one JSON entry per
// line, as VerifyFile reads it.
func (l *Log) Export(w io.Writer) error {
	rows, err := l.db.Query("SELECT event FROM audit ORDER BY id")
	if err != nil {
		return err
	}
	defer rows.Close()
	bw := bufio.NewWriter(w)
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return err
		}
		bw.WriteString(data)
		bw.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return bw.Flush()
}

// ImportFiles copies the log an older version kept in files of dir, its
// rotated files then audit.jsonl, into the state file, entries as they
// were: a chain broken in the files stays broken. A damaged end of the
// last file, as a write cut short leaves, is left out, and an entry says
// so. It returns the files read, which the caller sets aside once the
// import is committed, and the rows it wrote.
func ImportFiles(tx *sql.Tx, dir string) (files []string, imported int, err error) {
	if files, err = rotated(dir); err != nil {
		return nil, 0, err
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); err == nil {
		files = append(files, filepath.Join(dir, FileName))
	}
	var last Event
	for i, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, 0, err
		}
		for line, off := 1, 0; off < len(data); line++ {
			end := bytes.IndexByte(data[off:], '\n')
			complete := end >= 0
			if !complete {
				end = len(data) - off
			}
			var e Event
			if err := json.Unmarshal(data[off:off+end], &e); err != nil {
				if i == len(files)-1 {
					// A write cut short: what follows the last whole entry.
					l := Log{user: currentUser()}
					e := l.chain(Event{Kind: KindRecovered, Detail: fmt.Sprintf("%d damaged bytes after entry %d of %s were left out of the import; the file is kept as %s.migrated",
						len(data)-off, last.Seq, filepath.Base(path), filepath.Base(path))}, last.Seq, last.Hash)
					if err := insert(tx, e); err != nil {
						return nil, 0, err
					}
					imported++
					break
				}
				return nil, 0, fmt.Errorf("%s, line %d: the entry is not valid JSON; move the file aside to open the project", filepath.Base(path), line)
			}
			if err := insert(tx, e); err != nil {
				return nil, 0, err
			}
			last = e
			imported++
			off += end + 1
		}
	}
	return files, imported, nil
}
