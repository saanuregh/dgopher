package app

import (
	"strings"
	"testing"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// Whether a connection opened over TLS, as the server negotiated it, is
// shown after the server's version and recorded with the connect: under
// prefer, the mode alone does not say.
func TestTLSStateShown(t *testing.T) {
	testutil.Integration(t)
	pg := testutil.PGConfig()
	pg.TLS = db.TLSPrefer
	my := db.Config{ID: "my", Name: "Shop (MySQL)", Engine: db.MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dgopher", Database: "shop", TLS: db.TLSPrefer}
	for _, c := range []struct {
		cfg  db.Config
		want string
	}{{pg, "no TLS"}, {my, "TLS"}} {
		// One connection per app: the status bar shows the only one.
		a := newTestApp(t)
		tt := ui.NewTester(a.view, 1200, 760)
		cn := addConn(a, c.cfg)
		a.activate(navNode{kind: nodeConn, conn: cn.Config.ID})
		testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
		tt.Frame()
		shown := c.cfg.Engine.Label() + " " + strings.Split(cn.Version, "\n")[0] + ", " + c.want
		if !tt.HasText(shown) {
			t.Errorf("the status bar does not show %q: %q", shown, tt.Texts())
		}
		events, err := a.projects[0].Audit.Read(0)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, e := range events {
			if e.Kind == audit.KindConnect && e.Connection == c.cfg.Name {
				found = strings.HasSuffix(e.Detail, ", "+c.want)
				if !found {
					t.Errorf("the connect record of %s: %q", c.cfg.Name, e.Detail)
				}
			}
		}
		if !found {
			t.Errorf("no connect record of %s with %q", c.cfg.Name, c.want)
		}
	}
}
