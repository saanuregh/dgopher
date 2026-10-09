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
	Statement    string    `json:"statement,omitempty"`
	Rows         int64     `json:"rows,omitempty"`
	DurationMS   int64     `json:"durationMs,omitempty"`
	Error        string    `json:"error,omitempty"`
	Detail       string    `json:"detail,omitempty"`
	Prev         string    `json:"prev"`
	Hash         string    `json:"hash"`
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
}

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
