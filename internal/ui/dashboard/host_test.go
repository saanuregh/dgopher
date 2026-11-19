package dashboard

import (
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"
)

// testHost is the dataview fake with the dashboard's own hooks.
type testHost struct {
	*dataview.FakeHost
	retries int
}

func newTestHost(t *testing.T) *testHost { return &testHost{FakeHost: dataview.NewFakeHost(t)} }

func (h *testHost) ConnectionReport(cn *connection.Conn) widgets.ErrorReport {
	return widgets.ConnectionReport(cn, func() { h.retries++; h.Connect(cn, nil) }, nil)
}
