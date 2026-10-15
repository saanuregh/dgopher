package redis

import (
	"context"
	"errors"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dataview"

	"github.com/egoist/mygo/ui"
)

// A subscription or MONITOR the network ended starts again once the
// computer is online; one the user stopped stays stopped.
func TestPubSubRestartsWhenOnline(t *testing.T) {
	a := dataview.NewFakeHost(t)
	cn := a.AddConn(db.Config{ID: "r", Name: "Cache", Engine: db.Redis, Env: db.Production})
	r := &Tab{a: a, conn: cn}
	tt := ui.NewTester(a.View, 400, 200)

	subscribes, monitors := 0, 0
	dropped := errors.New("connection reset by peer")
	subscribe = func(_ *db.KV, ctx context.Context, channels, patterns []string, _ func(db.PubSubMessage)) error {
		subscribes++
		if subscribes == 1 {
			return dropped
		}
		<-ctx.Done()
		return nil
	}
	monitor = func(_ *db.KV, ctx context.Context, _ func(db.MonitorLine)) error {
		monitors++
		if monitors == 1 {
			return dropped
		}
		<-ctx.Done()
		return nil
	}
	t.Cleanup(func() { subscribe, monitor = (*db.KV).Subscribe, (*db.KV).Monitor })

	r.pubsub.listenIn = "news orders.*"
	r.listen()
	r.runMonitor()
	testutil.WaitFor(t, tt, "both to fail", func() bool {
		return r.pubsub.cancel == nil && r.pubsub.err != "" && r.monitor.cancel == nil && r.monitor.err != ""
	})
	r.pubsub.listenIn = "typed since"
	r.Online()
	if r.pubsub.cancel == nil || r.pubsub.listening != "news orders.*" {
		t.Fatalf("the subscription did not start again: listening %q", r.pubsub.listening)
	}
	if r.pubsub.listenIn != "typed since" {
		t.Fatalf("what was typed since became %q", r.pubsub.listenIn)
	}
	if r.monitor.cancel == nil || a.Confirm != nil {
		t.Fatalf("MONITOR did not start again without asking: confirm %+v", a.Confirm)
	}
	testutil.WaitFor(t, tt, "both to run", func() bool { return subscribes == 2 && monitors == 2 })

	// Stopped by the user: not started again.
	r.pubsub.cancel()
	r.monitor.cancel()
	testutil.WaitFor(t, tt, "both to stop", func() bool { return r.pubsub.cancel == nil && r.monitor.cancel == nil })
	r.Online()
	if r.pubsub.cancel != nil || r.monitor.cancel != nil {
		t.Fatal("a stop of the user's was undone")
	}
}
