package app

import (
	"fmt"
	"log"
	"strings"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/db"
	"dgopher/internal/redact"
)

// Record appends an event about a connection to the audit log. It takes
// the connection's settings by value, so goroutines may call it.
func (a *App) Record(cfg *db.Config, e audit.Event) {
	l := a.auditFor(cfg)
	if l == nil {
		return
	}
	if cfg != nil {
		e.Connection, e.ConnectionID = cfg.Name, cfg.ID
		e.Engine, e.Environment = string(cfg.Engine), string(cfg.Env)
		if e.Database == "" {
			e.Database = cfg.Database
		}
	}
	e.Statement = redact.Secrets(e.Statement)
	e.Error = redact.Secrets(e.Error)
	if err := l.Record(e); err != nil {
		log.Println("audit:", err)
	}
}

// RecordRun records a statement or command that ran.
func (a *App) RecordRun(cfg db.Config, kind, database, stmt string, rows int64, d time.Duration, err error) {
	e := audit.Event{Kind: kind, Database: database, Statement: stmt, Rows: rows, DurationMS: d.Milliseconds()}
	if err != nil {
		e.Error = err.Error()
	}
	a.Record(&cfg, e)
}

// connectionSummary describes the settings of a connection that matter to
// an auditor: where, how, and how carefully. It holds no secret.
func connectionSummary(cfg *db.Config) string {
	var parts []string
	if cfg.Engine.IsFile() {
		parts = append(parts, "file "+cfg.Database)
	} else {
		parts = append(parts, fmt.Sprintf("%s@%s:%d", cfg.User, cfg.Host, cfg.Port))
		tls := string(cfg.TLS)
		if tls == "" {
			tls = "disable"
		}
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
	}
	if cfg.ReadOnly {
		parts = append(parts, "read-only")
	}
	if cfg.ManualCommit() {
		parts = append(parts, "manual commit")
	}
	return strings.Join(parts, ", ")
}

// auditFor returns the audit log of the project of a connection.
func (a *App) auditFor(cfg *db.Config) *audit.Log {
	if cfg == nil {
		return nil
	}
	a.auditMu.Lock()
	defer a.auditMu.Unlock()
	for prefix, l := range a.auditLogs {
		if strings.HasPrefix(cfg.ID, prefix) {
			return l
		}
	}
	return nil
}
