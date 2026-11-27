package app

import (
	"fmt"
	"strings"

	"dgopher/internal/db"
)

// mongoWhere is where a MongoDB connection's password goes besides its
// host: the DNS SRV record, the other hosts and the replica set; "" for
// one server, and for an engine of another family, whose keys stay as
// they were.
func mongoWhere(cfg *db.Config) string {
	m := cfg.Mongo
	if !cfg.Engine.IsDocument() || !m.SRV && len(m.Hosts) == 0 && m.ReplicaSet == "" {
		return ""
	}
	return fmt.Sprintf("|mongo|%v|%q|%q", m.SRV, strings.Join(m.Hosts, ","), m.ReplicaSet)
}

// mongoSummary describes how a MongoDB connection finds its servers, as
// a connection's summary lists it; none for another family's.
func mongoSummary(cfg *db.Config) []string {
	if !cfg.Engine.IsDocument() {
		return nil
	}
	m := cfg.Mongo
	var parts []string
	if m.SRV {
		parts = append(parts, "DNS SRV")
	}
	if len(m.Hosts) > 0 {
		parts = append(parts, "hosts "+strings.Join(m.Hosts, ", "))
	}
	if m.ReplicaSet != "" {
		parts = append(parts, "replica set "+m.ReplicaSet)
	}
	if m.DirectConnection {
		parts = append(parts, "direct")
	}
	return parts
}
