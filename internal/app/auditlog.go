package app

import (
	"cmp"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/redact"
	"dgopher/internal/secretcmd"
	"dgopher/internal/store"
)

// Record appends an event about a connection to the audit log. It takes
// the connection's settings by value, so goroutines may call it.
func (a *App) Record(cfg *db.Config, e audit.Event) { a.record(cfg, e) }

// record writes an event, its secrets redacted, to the audit log of the
// connection's project, and returns the project and the event as written:
// Seq 0 when the project keeps no audit log.
func (a *App) record(cfg *db.Config, e audit.Event) (*project.Project, audit.Event) {
	p := a.projectOf(cfg)
	if p == nil {
		return nil, e
	}
	e = sealed(cfg, e.WithOrigin())
	if p.Audit != nil {
		seq, err := p.Audit.Record(e)
		if err != nil {
			log.Println("audit:", err)
		}
		e.Seq = seq
	}
	return p, e
}

// RecordRun records a statement or command that ran, which the app made.
func (a *App) RecordRun(cfg db.Config, kind, database, stmt string, rows int64, d time.Duration, err error) {
	// A pool's new connection runs the identity's command: what it
	// printed to stderr stays out of the log, as Record keeps it.
	a.Record(&cfg, audit.Event{Kind: kind, Database: database, Statement: stmt, Rows: rows, DurationMS: d.Milliseconds(), Err: err})
}

// RecordTyped records what a person wrote that ran, in the audit log and,
// when asked, in the project's query history, which keeps the number of
// the audit entry.
func (a *App) RecordTyped(cfg db.Config, t audit.TypedRun) {
	p, e := a.record(&cfg, t.Event())
	if p == nil || !t.History {
		return
	}
	sql := cmp.Or(e.Typed, e.Statement)
	if e.Params != "" {
		sql += "\n-- parameters: " + e.Params
	}
	h := store.HistoryEntry{Time: time.Now().Add(-t.Took), ConnectionID: cfg.ID, Connection: cfg.Name, Database: e.Database,
		SQL: sql, Duration: t.Took, Rows: t.Rows, Error: e.Error, AuditSeq: e.Seq}
	if err := p.Local.AppendHistory(h); err != nil {
		log.Println("history:", err)
	}
}

// sealed is an event as the audit log keeps it: with its connection, and
// its secrets redacted.
func sealed(cfg *db.Config, e audit.Event) audit.Event {
	if cfg != nil {
		e.Connection, e.ConnectionID = cfg.Name, cfg.ID
		e.Engine, e.Environment = string(cfg.Engine), string(cfg.Env)
		e.Database = cmp.Or(e.Database, cfg.Database)
	}
	if e.Err != nil && e.Error == "" {
		e.Error = secretcmd.AuditText(e.Err)
	}
	e.Error = redact.Error(redact.Error(e.Error, e.Statement), e.Typed)
	e.Statement, e.Typed, e.Params = redact.Secrets(e.Statement), redact.Secrets(e.Typed), redact.Secrets(e.Params)
	return e
}

// relabel has an open connection's traffic recorded under its name, ID
// and environment as they are now, changed without connecting again.
func relabel(cn *connection.Conn) {
	if cn.Store != nil {
		cn.Store.RecordAs(cn.Config)
	}
}

// trafficTextMax is how many bytes of a statement sent the traffic
// keeps: a large import's statements would otherwise fill the log with
// their values.
const trafficTextMax = 4096

// recordSent keeps what a connection sent as its project's audit traffic,
// redacted as it is written, not as it is sent; the values sent apart are
// counted, not kept.
func (a *App) recordSent(cfg *db.Config, s db.Sent) {
	p := a.projectOf(cfg)
	if p == nil || p.Audit == nil {
		return
	}
	connConfig := *cfg
	if s.Redis != nil {
		// Each argument is redacted on its own, so it may be cut first:
		// a large value is not kept, nor redacted whole.
		for i, arg := range s.Redis {
			s.Redis[i] = cutText(arg)
		}
	}
	p.Audit.Traffic(func() audit.Event {
		e := audit.Event{Database: s.Database, Statement: s.Text, DurationMS: s.Took.Milliseconds(), Err: s.Err}
		if s.Args > 0 {
			e.Params = fmt.Sprintf("%d sent apart", s.Args)
		}
		if s.Redis != nil {
			e.Statement = redact.Redis(s.Redis)
		}
		e = sealed(&connConfig, e)
		e.Statement = cutText(e.Statement)
		return e
	})
}

// cutText keeps the first trafficTextMax bytes of text, and its length.
func cutText(text string) string {
	if len(text) <= trafficTextMax {
		return text
	}
	cut := trafficTextMax
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return fmt.Sprintf("%s… (%d bytes)", text[:cut], len(text))
}

// connectionSummary describes the settings of a connection that matter to
// an auditor: where, how, and how carefully. It holds no secret.
func connectionSummary(cfg *db.Config) string {
	var parts []string
	if cfg.Engine.IsFile() {
		parts = append(parts, "file "+cfg.Database)
	} else {
		parts = append(parts, fmt.Sprintf("%s@%s:%d", cfg.User, cfg.Host, cfg.Port))
		tls := cmp.Or(string(cfg.TLS), "disable")
		parts = append(parts, "tls "+tls)
	}
	switch cfg.Redis.Mode {
	case db.RedisCluster:
		parts = append(parts, "cluster")
	case db.RedisSentinel:
		parts = append(parts, "sentinel master "+cfg.Redis.Master)
	}
	if cfg.Redis.Nodes != "" {
		parts = append(parts, "nodes "+cfg.Redis.Nodes)
	}
	if cfg.SSH.Enabled {
		parts = append(parts, "ssh "+cfg.SSH.User+"@"+cfg.SSH.Host)
	}
	switch sourceOf(cfg) {
	case sourceEnv:
		parts = append(parts, "password from $"+cfg.PasswordEnv)
	case sourceCommand:
		parts = append(parts, "password from command")
	case sourceAsk:
		parts = append(parts, "password asked")
	case sourceIdentity:
		parts = append(parts, "logged in as "+cfg.Identity.Label())
	}
	if cfg.ClearTextPassword {
		parts = append(parts, "clear-text password")
	}
	if cfg.Proxy.Kind != "" {
		parts = append(parts, cfg.Proxy.Kind+" proxy "+cfg.Proxy.Host)
	}
	if cfg.ReadOnly {
		parts = append(parts, "read-only")
	}
	if cfg.ManualCommit() {
		parts = append(parts, "manual commit")
	}
	return strings.Join(parts, ", ")
}

// projectOf returns the project of a connection, for its records.
func (a *App) projectOf(cfg *db.Config) *project.Project {
	if cfg == nil {
		return nil
	}
	a.auditMu.Lock()
	defer a.auditMu.Unlock()
	for prefix, p := range a.recordProjects {
		if strings.HasPrefix(cfg.ID, prefix) {
			return p
		}
	}
	return nil
}
