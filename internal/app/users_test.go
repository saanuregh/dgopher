package app

import (
	"testing"

	"dgopher/internal/db"

	"github.com/egoist/mygo/ui"
)

// MariaDB's roles have no host, so its New Role form asks none; its users
// and MySQL's roles still have one.
func TestNewRoleHost(t *testing.T) {
	for _, c := range []struct {
		engine db.Engine
		kind   accountFormKind
		host   bool
	}{
		{db.MariaDB, formNewRole, false},
		{db.MariaDB, formNewUser, true},
		{db.MySQL, formNewRole, true},
	} {
		a := newTestApp(t)
		cn := addConn(a, db.Config{ID: "u", Name: "u", Engine: c.engine, Host: "h"})
		ut := &usersTab{a: a, conn: cn, sel: -1}
		tt := ui.NewTester(func(c *ui.Context) {
			if ut.form != nil {
				ut.formView(c)
			}
		}, 1000, 800)
		ut.openForm(c.kind)
		tt.Frame()
		if tt.HasText("Host") != c.host {
			t.Errorf("%s form %v: Host shown %v: %q", c.engine, c.kind, !c.host, tt.Texts())
		}
	}
}
