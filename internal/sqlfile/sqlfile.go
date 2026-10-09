// Package sqlfile reads the statements of a file of SQL as it streams,
// as a database's dump, which may be larger than memory: statements as
// the editor splits them, and the rows a PostgreSQL dump gives its COPY
// statements on the lines after them.
package sqlfile

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"dgopher/internal/sqltext"
)

// Statement is a statement of the file.
type Statement struct {
	SQL  string
	Line int // where it starts, from 1
	// Copy is set for a COPY … FROM STDIN, whose rows follow it: Data
	// reads them, until the next statement is asked for.
	Copy bool
}

// Reader reads a file's statements one at a time.
type Reader struct {
	src     *bufio.Reader
	dialect sqltext.Dialect
	pending string // read, not yet given as statements
	line    int    // the line pending starts on
	eof     bool
	queue   []Statement
	// delimiter is MySQL's DELIMITER in effect where pending starts.
	delimiter string
	// copying is set while the rows of a COPY statement are the file's
	// next lines.
	copying bool
	read    int64 // bytes read from the file
}

// chunk is how much is read at a time, at least: a statement longer
// than what was read makes the next read longer, for reading it to stay
// linear in its length.
const chunk = 256 << 10

// NewReader reads the statements of src, split by d's lexing rules.
func NewReader(src io.Reader, d sqltext.Dialect) *Reader {
	return &Reader{src: bufio.NewReaderSize(src, chunk), dialect: d, line: 1}
}

// Read is how many bytes of the file were read so far, for progress.
func (r *Reader) Read() int64 { return r.read }

// more reads at least n more bytes into pending, fewer at the end.
func (r *Reader) more(n int) error {
	if r.eof {
		return nil
	}
	buf := make([]byte, n)
	got, err := io.ReadFull(r.src, buf)
	r.read += int64(got)
	r.pending += string(buf[:got])
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		r.eof = true
		return nil
	}
	return err
}

// copyStdin matches a COPY statement whose rows follow it in the file.
var copyStdin = regexp.MustCompile(`(?is)^\s*COPY\b.*\bFROM\s+STDIN\b`)

// Next returns the next statement, io.EOF at the end of the file. The
// rows of a COPY statement not read by Data are skipped.
func (r *Reader) Next() (Statement, error) {
	if r.copying {
		if _, err := io.Copy(io.Discard, r.Data()); err != nil {
			return Statement{}, err
		}
	}
	for len(r.queue) == 0 {
		if err := r.fill(); err != nil {
			return Statement{}, err
		}
		if len(r.queue) == 0 && r.eof && strings.TrimSpace(r.pending) == "" {
			return Statement{}, io.EOF
		}
	}
	st := r.queue[0]
	r.queue = r.queue[1:]
	if st.Copy {
		r.copying = true
	}
	return st, nil
}

// fill queues the statements complete in what was read, reading more as
// needed: a statement is complete once the next one starts, or the file
// ends.
func (r *Reader) fill() error {
	if err := r.skipPsqlCommands(); err != nil {
		return err
	}
	if strings.TrimSpace(r.pending) == "" {
		if r.eof {
			r.pending = ""
			return nil
		}
		return r.more(chunk)
	}
	stmts := sqltext.SplitWith(r.pending, r.dialect, sqltext.SplitOptions{Mode: sqltext.SemicolonOnly, Delimiter: r.delimiter})
	complete := len(stmts)
	if !r.eof {
		complete-- // the last may go on in what is not read yet
	}
	if complete <= 0 {
		if len(stmts) == 0 && r.eof {
			r.pending = "" // only comments are left
			return nil
		}
		return r.more(max(chunk, len(r.pending)))
	}
	// What was given is cut at the rune the next statement starts at, the
	// line counted as it goes; offsets count runes.
	at, pos, line := 0, 0, r.line
	advance := func(to int) {
		n := byteOffset(r.pending[at:], to-pos)
		line += strings.Count(r.pending[at:at+n], "\n")
		at, pos = at+n, to
	}
	queued := 0
	for _, s := range stmts[:complete] {
		if strings.HasPrefix(s.Text, `\`) {
			if queued == 0 {
				// Not at the start, where it would have been skipped: after
				// a comment of another kind.
				advance(s.Start)
				return fmt.Errorf("line %d: %s is a command of psql, which only psql runs", line, strings.Fields(s.Text)[0])
			}
			break // a psql command: read from it on, once the statements before go
		}
		advance(s.Start)
		st := Statement{SQL: s.Text, Line: line}
		queued++
		if r.dialect == sqltext.Postgres && copyStdin.MatchString(s.Text) {
			// Its rows start on the line after its semicolon.
			advance(s.End)
			rest := r.pending[at:]
			if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
				at, line = at+nl+1, line+1
			} else {
				at = len(r.pending)
			}
			st.Copy = true
			r.queue = append(r.queue, st)
			r.pending, r.line = r.pending[at:], line
			return nil
		}
		r.queue = append(r.queue, st)
	}
	if queued < len(stmts) {
		advance(stmts[queued].Start)
		r.delimiter = stmts[queued].Delimiter
	} else {
		at, line = len(r.pending), line+strings.Count(r.pending[at:], "\n")
	}
	r.pending, r.line = r.pending[at:], line
	return nil
}

// skipPsqlCommands reads past the psql commands that start what is
// left, with the comments before them. A dump of PostgreSQL 17.6 and
// later begins and ends with \restrict and \unrestrict, which keep psql
// from running commands a hostile dump would hide in its data; without
// psql they mean nothing. Other commands, as \connect, are psql's to
// run: the file stops there.
func (r *Reader) skipPsqlCommands() error {
	for {
		body := stripComments(r.pending)
		if !strings.HasPrefix(body, `\`) {
			return nil
		}
		r.line += strings.Count(r.pending[:len(r.pending)-len(body)], "\n")
		r.pending = body
		nl := strings.IndexByte(r.pending, '\n')
		for nl < 0 && !r.eof {
			if err := r.more(chunk); err != nil {
				return err
			}
			nl = strings.IndexByte(r.pending, '\n')
		}
		if nl < 0 {
			nl = len(r.pending)
		}
		cmd := strings.TrimSpace(r.pending[:nl])
		if word, _, _ := strings.Cut(cmd, " "); word != `\restrict` && word != `\unrestrict` {
			return fmt.Errorf("line %d: %s is a command of psql, which only psql runs", r.line, word)
		}
		r.pending = r.pending[min(nl+1, len(r.pending)):]
		r.line++
	}
}

// stripComments drops the blank lines and -- comments text starts with.
func stripComments(text string) string {
	for {
		t := strings.TrimLeft(text, " \t\r\n")
		if !strings.HasPrefix(t, "--") {
			return t
		}
		nl := strings.IndexByte(t, '\n')
		if nl < 0 {
			return t // a comment not read to its end yet
		}
		text = t[nl+1:]
	}
}

// byteOffset is where the rune of index runes starts in s.
func byteOffset(s string, runes int) int {
	at := 0
	for i := 0; i < runes && at < len(s); i++ {
		_, size := utf8.DecodeRuneInString(s[at:])
		at += size
	}
	return at
}

// Data reads the rows of the COPY statement last given, in the file's
// text: its lines up to the one holding \. alone, which ends them.
func (r *Reader) Data() io.Reader { return &copyData{r: r} }

type copyData struct {
	r    *Reader
	line []byte // what is left of the line read last
}

func (c *copyData) Read(p []byte) (int, error) {
	r := c.r
	for len(c.line) == 0 {
		if !r.copying {
			return 0, io.EOF
		}
		nl := strings.IndexByte(r.pending, '\n')
		for nl < 0 && !r.eof {
			if err := r.more(chunk); err != nil {
				return 0, err
			}
			nl = strings.IndexByte(r.pending, '\n')
		}
		if nl < 0 {
			if r.pending == "" {
				return 0, fmt.Errorf("line %d: the rows of a COPY end without \\.", r.line)
			}
			nl = len(r.pending) - 1
		}
		line := r.pending[:nl+1]
		r.pending = r.pending[nl+1:]
		r.line++
		if strings.TrimRight(line, "\r\n") == `\.` {
			r.copying = false
			return 0, io.EOF
		}
		c.line = []byte(line)
	}
	n := copy(p, c.line)
	c.line = c.line[n:]
	return n, nil
}
