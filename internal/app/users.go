package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// usersTab manages the users and roles of a server, and what they may do.
type usersTab struct {
	a    *App
	conn *connection.Conn

	accounts []db.Account
	sel      int // the account chosen, -1 for none
	selName  string
	privs    []db.Privilege
	loading  bool
	err      string
	// form is the dialog making or changing an account, nil when none.
	form *accountForm
}

// accountForm is a dialog of the Users tab, by what it does.
type accountForm struct {
	open    bool
	kind    accountFormKind
	account db.Account // the account it changes

	name, host         string
	password, repeated string
	createDB, create   bool // a new user may create databases, roles
	privileges         map[string]bool
	schema, table      string
	role               string
	err                string
}

type accountFormKind int

const (
	formNewUser accountFormKind = iota
	formNewRole
	formPassword
	formGrant
	formJoinRole
)

func newUsersTab(a *App, cn *connection.Conn) *usersTab {
	t := &usersTab{a: a, conn: cn, sel: -1}
	t.refresh()
	return t
}

func (t *usersTab) Title() string                { return t.conn.Config.Name + " · users" }
func (t *usersTab) Connection() *connection.Conn { return t.conn }
func (t *usersTab) CloseReason() string          { return "" }
func (t *usersTab) Close()                       {}

// openUsers opens the users of a server, or brings their tab forward.
func (a *App) openUsers(cn *connection.Conn) {
	if !db.UsersSupported(cn.Config.Engine) {
		a.ShowError("No users to manage", cn.Config.Engine.Label()+" has no users DGopher manages.")
		return
	}
	if a.ActivateTab(func(t widgets.Tab) bool { ut, ok := t.(*usersTab); return ok && ut.conn == cn }) {
		return
	}
	a.Connect(cn, func() { a.AddTab(newUsersTab(a, cn)) })
}

// refresh reads the accounts again, and the chosen one's privileges.
func (t *usersTab) refresh() {
	pool := t.conn.DB
	if pool == nil || t.loading {
		return
	}
	t.loading = true
	dataview.BackgroundResetOnPanic(t.a, func() { t.loading = false }, func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		accounts, err := db.ListAccounts(ctx, pool.Dialect, pool.SQL)
		return func() {
			t.loading = false
			if err != nil {
				t.err = err.Error()
				return
			}
			t.err, t.accounts = "", accounts
			t.sel = slices.IndexFunc(accounts, func(a db.Account) bool { return a.Label() == t.selName })
			t.loadPrivileges()
		}
	})
}

// loadPrivileges reads what the chosen account may do.
func (t *usersTab) loadPrivileges() {
	t.privs = nil
	if t.sel < 0 {
		return
	}
	pool, acc := t.conn.DB, t.accounts[t.sel]
	t.a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		privs, err := db.AccountPrivileges(ctx, pool.Dialect, pool.SQL, acc)
		return func() {
			if t.sel < 0 || t.accounts[t.sel].Label() != acc.Label() {
				return // another account is chosen now
			}
			if err != nil {
				t.err = err.Error()
				return
			}
			t.privs = privs
		}
	})
}

// apply runs statements changing accounts once the user agrees, always:
// they change who may do what. They run on a session of their own, never
// in an editor, whose file a password would be saved in.
func (t *usersTab) apply(title string, stmts []string) {
	t.a.applyStatements(t.conn, "", title, stmts, "This changes who may do what on "+t.conn.Config.Name+".", func(err error) {
		if err != nil {
			t.a.ShowError(title+" failed", err.Error())
		} else {
			t.a.toast = &pendingToast{text: "Done: " + title}
		}
		t.refresh()
	})
}

func (t *usersTab) View(c *ui.Context) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	ui.Column(c).Grow(1).Children(func() {
		ui.Row(c).Padding(6, 10).Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ui.Icon(c, widgets.IconUsers).TextColor(pal.Muted).FontSize(14)
			ui.Text(c, "Users and roles").Bold()
			ui.Text(c, fmt.Sprintf("%d", len(t.accounts))).FontSize(12).TextColor(pal.Muted)
			if t.loading {
				ui.Spinner(c).Size(12, 12)
			}
			ui.Spacer(c)
			if ui.Button(c, "New User…").Clicked() {
				t.openForm(formNewUser)
			}
			if ui.Button(c, "New Role…").Clicked() {
				t.openForm(formNewRole)
			}
			if widgets.ToolButton(c, widgets.IconRefresh, "Refresh", "Read the users again").Clicked() {
				t.refresh()
			}
		})
		if t.err != "" {
			ui.Text(c, t.err).TextColor(th.Danger).Padding(10).Selectable()
		}
		ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
			ui.Scroll(c).Width(280).BorderWidth(0, 1, 0, 0).BorderColor(th.Border).Children(func() {
				ui.Column(c).Padding(6).Gap(2).Children(func() {
					for i, acc := range t.accounts {
						t.accountRow(c, i, acc)
					}
				})
			})
			ui.Column(c).Grow(1).Children(func() {
				if t.sel < 0 {
					ui.Text(c, "Choose a user or a role.").TextColor(pal.Muted).Padding(16)
					return
				}
				t.accountView(c, t.accounts[t.sel])
			})
		})
	})
	if t.form != nil {
		t.formView(c)
	}
}

func (t *usersTab) accountRow(c *ui.Context, i int, acc db.Account) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	row := ui.ButtonBase(c.Key("account-"+acc.Label())).Padding(6, 8).Radius(6).Label(acc.Label())
	if i == t.sel {
		row.Background(th.Accent.Alpha(0.15))
	} else if row.Hovered() {
		row.Background(pal.Hover)
	}
	row.Children(func() {
		icon := widgets.IconUser
		if acc.Role {
			icon = widgets.IconUsers
		}
		ui.Icon(c, icon).FontSize(13).TextColor(pal.Muted)
		ui.Text(c, acc.Label()).SingleLine().Grow(1).Shrink(1)
		if acc.Role {
			ui.Text(c, "role").FontSize(11).TextColor(pal.Muted)
		}
	})
	if row.Clicked() && i != t.sel {
		t.sel, t.selName = i, acc.Label()
		t.loadPrivileges()
	}
}

// accountView shows an account: what it is, the roles it belongs to, and
// what it may do, each privilege with the means to take it back.
func (t *usersTab) accountView(c *ui.Context, acc db.Account) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	d := t.conn.DB.Dialect
	ui.Row(c).Padding(10, 16).Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
		ui.Text(c, acc.Label()).FontSize(16).Bold().Grow(1).Shrink(1)
		if !acc.Role && ui.Button(c, "Change Password…").Clicked() {
			t.openForm(formPassword)
		}
		if ui.Button(c, "Grant…").Clicked() {
			t.openForm(formGrant)
		}
		if ui.Button(c, "Add to Role…").Clicked() {
			t.openForm(formJoinRole)
		}
		if ui.Button(c, "Drop…").Clicked() {
			t.apply("Drop "+acc.Label(), db.DropAccountSQL(d, acc))
		}
	})
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(12, 16).Gap(10).Children(func() {
			if len(acc.Attributes) > 0 {
				ui.Text(c, strings.Join(acc.Attributes, " · ")).TextColor(pal.Muted)
			}
			if len(acc.MemberOf) > 0 {
				ui.Text(c, "Member of "+strings.Join(acc.MemberOf, ", "))
			}
			ui.Text(c, "Privileges").Bold()
			if t.conn.Config.Engine == db.Postgres {
				ui.Text(c, "Those on the database's own tables and schemas, and on its databases.").FontSize(12).TextColor(pal.Muted)
			}
			if len(t.privs) == 0 {
				ui.Text(c, "None.").TextColor(pal.Muted)
			}
			for i, p := range t.privs {
				ui.Row(c.Key(fmt.Sprint("priv-", i))).Gap(8).Children(func() {
					ui.Text(c, p.Text).Font(widgets.MonoFont).FontSize(12).Grow(1).Shrink(1).Selectable()
					if p.Revoke != "" && ui.Button(c, "Revoke").Label("Revoke "+p.Text).Clicked() {
						t.apply("Revoke from "+acc.Label(), []string{p.Revoke})
					}
				})
			}
		})
	})
}

func (t *usersTab) openForm(kind accountFormKind) {
	f := &accountForm{open: true, kind: kind, privileges: map[string]bool{"SELECT": true}, host: "%"}
	if t.sel >= 0 {
		f.account = t.accounts[t.sel]
	}
	f.schema = t.conn.DefaultSchema
	t.form = f
}

// formView shows the form open: what it asks depends on what it does.
func (t *usersTab) formView(c *ui.Context) {
	f := t.form
	th := c.Theme()
	e := t.conn.Config.Engine
	titles := map[accountFormKind]string{formNewUser: "New User", formNewRole: "New Role",
		formPassword: "Change the Password of " + f.account.Label(), formGrant: "Grant to " + f.account.Label(),
		formJoinRole: "Add " + f.account.Label() + " to a Role"}
	ui.Modal(c, &f.open, func() {
		ui.Column(c).Width(520).Gap(12).Children(func() {
			ui.Text(c, titles[f.kind]).FontSize(15).Bold()
			ui.Form(c, func() {
				switch f.kind {
				case formNewUser, formNewRole:
					ui.Field(c, "Name", func() { ui.TextInput(c, &f.name).AutoFocus() })
					if e == db.MySQL {
						ui.Field(c, "Host", func() { ui.TextInput(c, &f.host) }).Description("Where it may connect from: % for anywhere.")
					}
					if f.kind == formNewUser {
						t.passwordFields(c, f)
						if e == db.Postgres {
							ui.Field(c, "", func() { ui.Checkbox(c, &f.createDB, "May create databases") })
							ui.Field(c, "", func() { ui.Checkbox(c, &f.create, "May create roles") })
						}
					}
				case formPassword:
					t.passwordFields(c, f)
				case formGrant:
					ui.Field(c, "Privileges", func() {
						ui.Row(c).Gap(10).Wrap().Children(func() {
							for _, p := range db.Privileges(e) {
								on := f.privileges[p]
								if ui.Checkbox(c, &on, p).Changed() {
									f.privileges[p] = on
								}
							}
						})
					})
					label := "Schema"
					if e != db.Postgres {
						label = "Database"
					}
					ui.Field(c, label, func() { ui.TextInput(c, &f.schema).Font(widgets.MonoFont) })
					ui.Field(c, "Table", func() { ui.TextInput(c, &f.table).Font(widgets.MonoFont).Placeholder("every table") })
				case formJoinRole:
					var roles []string
					for _, acc := range t.accounts {
						if acc.Role && acc.Label() != f.account.Label() {
							roles = append(roles, acc.Label())
						}
					}
					if len(roles) == 0 {
						ui.Text(c, "There is no role yet: make one with New Role.")
						break
					}
					if f.role == "" {
						f.role = roles[0]
					}
					ui.Field(c, "Role", func() { ui.Select(c, &f.role, roles).Label("Role") })
				}
			})
			if f.err != "" {
				ui.Text(c, f.err).TextColor(th.Danger)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Cancel").Clicked() {
					f.open = false
				}
				if widgets.Activated(c, ui.PrimaryButton(c, "Continue")) {
					t.submit(f)
				}
			})
		})
	})
	if !f.open && t.form == f {
		t.form = nil
	}
}

func (t *usersTab) passwordFields(c *ui.Context, f *accountForm) {
	ui.Field(c, "Password", func() { ui.TextInput(c, &f.password).Password() })
	ui.Field(c, "Again", func() { ui.TextInput(c, &f.repeated).Password() })
}

// submit turns the form into statements, which apply confirms and runs.
func (t *usersTab) submit(f *accountForm) {
	d := t.conn.DB.Dialect
	f.err = ""
	if (f.kind == formNewUser || f.kind == formPassword) && f.password != f.repeated {
		f.err = "The passwords differ."
		return
	}
	var title string
	var stmts []string
	var err error
	switch f.kind {
	case formNewUser, formNewRole:
		name := strings.TrimSpace(f.name)
		if name == "" {
			f.err = "Name it."
			return
		}
		acc := db.Account{Name: name, Role: f.kind == formNewRole}
		if d.Engine() == db.MySQL {
			acc.Host = strings.TrimSpace(f.host)
		}
		var stmt string
		stmt, err = db.CreateAccountSQL(d, db.NewAccount{Account: acc, Password: f.password, CreateDB: f.createDB, CreateRole: f.create})
		title, stmts = "Create "+acc.Label(), []string{stmt}
		t.selName = acc.Label()
	case formPassword:
		var stmt string
		stmt, err = db.PasswordSQL(d, f.account, f.password)
		title, stmts = "Change the password of "+f.account.Label(), []string{stmt}
	case formGrant:
		var privs []string
		for _, p := range db.Privileges(d.Engine()) {
			if f.privileges[p] {
				privs = append(privs, p)
			}
		}
		stmts, err = db.GrantSQL(d, privs, strings.TrimSpace(f.schema), strings.TrimSpace(f.table), f.account)
		title = "Grant to " + f.account.Label()
	case formJoinRole:
		i := slices.IndexFunc(t.accounts, func(a db.Account) bool { return a.Label() == f.role })
		if i < 0 {
			f.err = "Choose the role."
			return
		}
		title, stmts = "Add "+f.account.Label()+" to "+f.role, []string{db.GrantRoleSQL(d, t.accounts[i], f.account)}
	}
	if err != nil {
		f.err = err.Error()
		return
	}
	f.open = false
	t.apply(title, stmts)
}
