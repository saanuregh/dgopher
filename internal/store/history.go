package store

import "time"

// HistoryEntry is one executed query, kept in a project's state file
// (package state).
type HistoryEntry struct {
	Time         time.Time     `json:"time"`
	ConnectionID string        `json:"connectionId"`
	Connection   string        `json:"connection"`
	Database     string        `json:"database,omitempty"`
	SQL          string        `json:"sql"`
	Duration     time.Duration `json:"duration"`
	Rows         int64         `json:"rows"`
	Error        string        `json:"error,omitempty"`
	// AuditSeq is the number of the entry the audit log has of the same
	// run; 0 when there is none, as for entries older than the link.
	AuditSeq int64 `json:"auditSeq,omitempty"`
}
