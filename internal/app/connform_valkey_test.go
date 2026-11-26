package app

import (
	"slices"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// A cluster's form asks a database index for Valkey, whose clusters may
// hold databases, and not for Redis, whose have database 0 only.
func TestClusterFormDatabaseField(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1200, 900)
	for engine, shown := range map[db.Engine]bool{db.Redis: false, db.Valkey: true} {
		a.openConnForm(nil)
		a.syncEngineChoice(a.connForm, engine)
		a.connForm.redisMode = slices.Index(redisModes, db.RedisCluster)
		testutil.Settled(t, tt, "Topology")
		testutil.Snapshot(t, tt, "connform-"+string(engine)+"-cluster")
		if got := tt.HasText("Database index"); got != shown {
			t.Errorf("%s cluster: database index shown %v, want %v", engine, got, shown)
		}
		// What the field says is what Save and Test Connection use.
		a.connForm.cfg.Database = "3"
		if got := a.connForm.config().Database; got != map[bool]string{true: "3", false: ""}[shown] {
			t.Errorf("%s cluster: database %q saved", engine, got)
		}
		a.connForm = nil
		tt.Frame()
	}
}
