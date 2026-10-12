// Package audit keeps a tamper-evident record of what was done to the
// databases, in a project's state file: every entry carries the hash of
// the entry before it, so that an entry changed, removed or inserted
// afterwards breaks the chain, which Verify finds.
package audit

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Kinds of events.
const (
	KindConnect    = "connect"
	KindDisconnect = "disconnect"
	KindStatement  = "statement" // SQL run from an editor
	KindEdit       = "edit"      // a statement generated from grid changes
	KindImport     = "import"
	KindScript     = "script"    // a file of SQL run without an editor
	KindCommand    = "command"   // a Redis command
	KindKill       = "kill"      // a query cancelled or a session ended
	KindConfirm    = "confirm"   // the user confirmed what the safety policy asked about
	KindBlocked    = "blocked"   // the safety policy refused
	KindTrust      = "trust"     // an SSH host key trusted
	KindConfig     = "config"    // a connection added, changed or deleted
	KindExport     = "export"    // rows written to a file or copied
	KindBackup     = "backup"    // a database backed up
	KindRestore    = "restore"   // a backup restored into a database
	KindRecovered  = "recovered" // a damaged end of the log set aside
	KindTraffic    = "traffic"   // a statement or command as it went to the server
)

// Who wrote a statement or command.
const (
	OriginTyped = "typed" // a person: in an editor, the console, a file or a dashboard panel
	OriginApp   = "app"   // the app, for what was asked of it
)

// Event is an entry of the log.
type Event struct {
	Seq          int64     `json:"seq"`
	Time         time.Time `json:"time"`
	User         string    `json:"user"`
	Kind         string    `json:"kind"`
	Connection   string    `json:"connection,omitempty"`
	ConnectionID string    `json:"connectionId,omitempty"`
	Engine       string    `json:"engine,omitempty"`
	Environment  string    `json:"environment,omitempty"`
	Database     string    `json:"database,omitempty"`
	// Statement is the text sent, its secrets redacted.
	Statement string `json:"statement,omitempty"`
	// Typed is the text as written, when the app changed it before
	// sending it, as with a ${var} or :name.
	Typed string `json:"typed,omitempty"`
	// Params lists the values sent apart from the statement, or, for
	// traffic, how many there were.
	Params string `json:"params,omitempty"`
	// Purpose says why the app sent a statement of its own.
	Purpose    string `json:"purpose,omitempty"`
	Origin     string `json:"origin,omitempty"`
	Rows       int64  `json:"rows,omitempty"`
	DurationMS int64  `json:"durationMs,omitempty"`
	Error      string `json:"error,omitempty"`
	Detail     string `json:"detail,omitempty"`
	Prev       string `json:"prev"`
	Hash       string `json:"hash"`

	// Err is the error of what was done, which the sink writes into Error
	// as secretcmd.AuditText does, without what a command printed to
	// stderr. It is never stored or hashed.
	Err error `json:"-"`
}

// TypedRun is a run of text a person wrote, as the code that ran it knows it.
type TypedRun struct {
	Kind, Database string
	Sent           string // the text sent
	Typed          string // the text as written, when the app changed it; "" when it is Sent
	Params         string // the values sent with it, as shown
	Rows           int64
	Took           time.Duration
	Err            error
	// History keeps it in the project's query history too, to run again.
	History bool
}

// Event is the audit entry of a typed run.
func (t TypedRun) Event() Event {
	return Event{Kind: t.Kind, Database: t.Database, Statement: t.Sent, Typed: t.Typed, Params: t.Params,
		Origin: OriginTyped, Rows: t.Rows, DurationMS: t.Took.Milliseconds(), Err: t.Err}
}

// WithOrigin names the app as the writer of a statement, edit or command
// whose writer is not named: only what a person typed says so.
func (e Event) WithOrigin() Event {
	if e.Origin == "" && (e.Kind == KindStatement || e.Kind == KindEdit || e.Kind == KindCommand) {
		e.Origin = OriginApp
	}
	return e
}

// hashOf is the hash of an event: of its JSON without the hash itself,
// which includes the hash of the event before it.
func hashOf(e Event) string {
	e.Hash = ""
	data, _ := json.Marshal(e)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Log appends events to the audit table of a state file (created by
// package state): one row per entry, in the order written.
type Log struct {
	db   *sql.DB
	user string

	// write keeps one writer at a time, so traffic waiting is written
	// before an entry recorded after it.
	write sync.Mutex
	// mu guards the traffic waiting to be written.
	mu      sync.Mutex
	pending []waiting
	dropped int
	writing bool // a write of a full batch has started
	timer   *time.Timer
}

// How traffic waits: written within trafficDelay, or at once when
// trafficBatch entries wait; past trafficMax, it is counted, not kept.
var (
	trafficDelay = 250 * time.Millisecond
	trafficBatch = 256
	trafficMax   = 10000
)

// Verification is what Verify found.
type Verification struct {
	Entries int
	// Broken is the number of the first entry whose hash or chain does
	// not hold, 0 when every entry does.
	Broken int64
	Reason string
}

// Intact reports a chain that holds from its first entry to its last.
func (v Verification) Intact() bool { return v.Reason == "" }

// FileName is the file an older version kept the log in, in the
// project's .dgopher folder, before it moved into the state file.
const FileName = "audit.jsonl"

// rotated lists the files an older version's log rotated into, oldest first.
func rotated(dir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "audit-[0-9]*.jsonl"))
	sort.Strings(files)
	return files, err
}
