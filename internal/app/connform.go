package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/netproxy"
	"dgopher/internal/project"
	"dgopher/internal/safety"
	"dgopher/internal/secretcmd"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// connForm edits a connection.
type connForm struct {
	open    bool
	editing *connection.Conn // nil for a new connection
	cfg     db.Config

	engine    string
	engineIdx int
	port      string
	sshPort   string
	env       int
	tls       string
	commit    string
	page      int // the page shown: pageGeneral, pageOptions or pageNetwork
	redisMode int // an index of redisModes
	proxy     int // an index of proxyKinds
	proxyPort string

	project    *project.Project // where a new connection goes
	projectSel string           // its label in the project choice
	source     string           // where the password comes from: a passwordSources label
	sshSource  string           // where the SSH secret comes from: keychain or command
	timeout    float64          // statement timeout, seconds, 0 for none
	idleMin    float64          // idle transaction timeout, minutes, 0 for the environment's
	idleNever  bool
	loaded     db.Config // the connection as it was, secrets included
	url        string
	urlErr     string
	urlFocus   bool
	testing    bool
	testResult string
	testOK     bool
	err        string
}

var commitLabels = []string{"Default (manual on production)", "Auto-commit", "Manual commit"}

// redisModes are the ways a Redis connection reaches its data, as the
// form offers them, with their labels.
var redisModes = []db.RedisMode{db.RedisStandalone, db.RedisCluster, db.RedisSentinel}
var redisModeLabels = []string{"Single server", "Cluster", "Sentinel"}
var tlsLabels = map[db.TLSMode]string{
	db.TLSDisable:    "Off",
	db.TLSPrefer:     "Prefer (unverified)",
	db.TLSRequire:    "Require (unverified)",
	db.TLSVerifyFull: "Verify certificate and host",
}

func engineByLabel(label string) db.Engine {
	for _, e := range db.Engines() {
		if e.Label() == label {
			return e
		}
	}
	return db.Postgres
}

// openConnForm edits a connection, or makes a new one in the current
// project; without a project, it asks for one first.
func (a *App) openConnForm(cn *connection.Conn) {
	if cn == nil && len(a.usableProjects()) == 0 {
		a.openNewProject(func(*project.Project) { a.openConnForm(nil) })
		return
	}
	f := &connForm{open: true, editing: cn, project: a.currentProject()}
	if cn != nil {
		f.project = cn.Project
		f.cfg = cn.Config
		// The password shows only from the keychain: one from a variable
		// must not end up there if the source changes.
		loaded := cn.Config
		a.loadSecrets(&loaded)
		f.cfg.SSH.Password, f.cfg.SSH.KeyPassphrase = loaded.SSH.Password, loaded.SSH.KeyPassphrase
		f.cfg.Redis.SentinelPassword = loaded.Redis.SentinelPassword
		f.cfg.Proxy.Password = loaded.Proxy.Password
		if sourceOf(&f.cfg) == sourceKeychain {
			f.cfg.Password = loaded.Password
		}
	} else {
		f.cfg = db.Config{Engine: db.Postgres, Host: "localhost", User: "postgres", Env: db.Development, TLS: db.TLSPrefer}
	}
	if f.project != nil && f.project.Err != "" {
		f.project = nil
	}
	if f.project == nil {
		if ps := a.usableProjects(); len(ps) > 0 {
			f.project = ps[0]
		}
	}
	f.projectSel = a.projectLabel(f.project)
	f.timeout = float64(f.cfg.StatementTimeout)
	f.idleNever = f.cfg.IdleTxTimeout < 0
	if f.cfg.IdleTxTimeout > 0 {
		f.idleMin = float64(f.cfg.IdleTxTimeout) / 60
	}
	f.source = passwordSources[sourceOf(&f.cfg)]
	f.redisMode = max(0, slices.Index(redisModes, f.cfg.Redis.Mode))
	f.sshSource = sshSources[0]
	if f.cfg.SSH.PasswordCommand != "" {
		f.sshSource = sshSources[1]
	}
	f.engine = f.cfg.Engine.Label()
	for i, e := range db.Engines() {
		if e == f.cfg.Engine {
			f.engineIdx = i
		}
	}
	f.port = ""
	if f.cfg.Port > 0 {
		f.port = strconv.Itoa(f.cfg.Port)
	}
	if f.cfg.SSH.Port > 0 {
		f.sshPort = strconv.Itoa(f.cfg.SSH.Port)
	}
	f.proxy = max(0, slices.Index(proxyKinds, f.cfg.Proxy.Kind))
	if f.cfg.Proxy.Port > 0 {
		f.proxyPort = strconv.Itoa(f.cfg.Proxy.Port)
	}
	for i, e := range db.Environments() {
		if e == f.cfg.Env {
			f.env = i
		}
	}
	if f.cfg.TLS == "" {
		f.cfg.TLS = db.TLSDisable
	}
	f.tls = tlsLabels[f.cfg.TLS]
	f.commit = commitLabels[0]
	switch f.cfg.Commit {
	case db.CommitAuto:
		f.commit = commitLabels[1]
	case db.CommitManual:
		f.commit = commitLabels[2]
	}
	if cn != nil {
		f.loaded = f.config()
	}
	a.connForm = f
}

// config returns the connection the form describes.
func (f *connForm) config() db.Config {
	cfg := f.cfg
	cfg.Engine = engineByLabel(f.engine)
	cfg.Port, _ = strconv.Atoi(strings.TrimSpace(f.port))
	cfg.SSH.Port, _ = strconv.Atoi(strings.TrimSpace(f.sshPort))
	cfg.Env = db.Environments()[f.env]
	for mode, label := range tlsLabels {
		if label == f.tls {
			cfg.TLS = mode
		}
	}
	switch f.commit {
	case commitLabels[1]:
		cfg.Commit = db.CommitAuto
	case commitLabels[2]:
		cfg.Commit = db.CommitManual
	default:
		cfg.Commit = db.CommitDefault
	}
	cfg.Name = strings.TrimSpace(cfg.Name)
	cfg.Host = strings.TrimSpace(cfg.Host)
	cfg.StatementTimeout = int(f.timeout)
	switch {
	case f.idleNever:
		cfg.IdleTxTimeout = -1
	case f.idleMin > 0:
		cfg.IdleTxTimeout = int(f.idleMin * 60)
	default:
		cfg.IdleTxTimeout = 0
	}
	// Only the chosen source is kept: a password typed, then a switch to
	// a command, is not saved.
	cfg.AskPassword = false
	switch sourceByLabel(f.source) {
	case sourceKeychain:
		cfg.PasswordEnv, cfg.PasswordCommand = "", ""
	case sourceEnv:
		cfg.Password, cfg.PasswordCommand = "", ""
		cfg.PasswordEnv = strings.TrimSpace(cfg.PasswordEnv)
	case sourceCommand:
		cfg.Password, cfg.PasswordEnv = "", ""
		cfg.PasswordCommand = strings.TrimSpace(cfg.PasswordCommand)
	case sourceAsk:
		cfg.Password, cfg.PasswordEnv, cfg.PasswordCommand = "", "", ""
		cfg.AskPassword = true
	}
	if f.sshSource == sshSources[1] {
		cfg.SSH.Password, cfg.SSH.KeyPassphrase = "", ""
		cfg.SSH.PasswordCommand = strings.TrimSpace(cfg.SSH.PasswordCommand)
	} else {
		cfg.SSH.PasswordCommand = ""
	}
	if cfg.Name == "" {
		if cfg.Engine.IsFile() {
			cfg.Name = baseName(cfg.Database)
		} else if cfg.Host != "" {
			cfg.Name = cfg.Host
		}
	}
	if cfg.Engine.IsFile() {
		cfg.Host, cfg.Port, cfg.User, cfg.SSH.Enabled, cfg.TLS = "", 0, "", false, db.TLSDisable
		cfg.Password, cfg.PasswordEnv, cfg.PasswordCommand, cfg.AskPassword = "", "", "", false
	}
	// Only what the chosen topology uses is kept.
	cfg.Proxy.Kind = proxyKinds[f.proxy]
	cfg.Proxy.Host = strings.TrimSpace(cfg.Proxy.Host)
	cfg.Proxy.Port, _ = strconv.Atoi(strings.TrimSpace(f.proxyPort))
	if cfg.Proxy.Kind == "" || cfg.Engine.IsFile() {
		cfg.Proxy = db.ProxyConfig{}
	}
	cfg.Redis.Mode = redisModes[f.redisMode]
	cfg.Redis.Nodes, cfg.Redis.Master = strings.TrimSpace(cfg.Redis.Nodes), strings.TrimSpace(cfg.Redis.Master)
	switch {
	case cfg.Engine != db.Redis:
		cfg.Redis = db.RedisConfig{}
	case cfg.Redis.Mode == db.RedisStandalone:
		cfg.Redis = db.RedisConfig{}
	case cfg.Redis.Mode == db.RedisCluster:
		cfg.Redis.Master, cfg.Redis.SentinelUser, cfg.Redis.SentinelPassword = "", "", ""
		cfg.Database = ""
	}
	return cfg
}

func baseName(p string) string {
	p = strings.TrimRight(p, "/\\")
	if i := strings.LastIndexAny(p, "/\\"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func (a *App) connFormView(c *ui.Context) {
	f := a.connForm
	t := c.Theme()
	engine := engineByLabel(f.engine)
	ui.DialogBase(c, &f.open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.45))
		_, winH := c.Size()
		panel.Width(680).Height(min(800, winH-40)).Radius(12).Background(t.Background).Border(1, t.Border).
			Shadow(0, 12, 40, 0, ui.RGBA(0, 0, 0, 0.3)).Role(ui.RoleDialog).Label("Connection")
		title := "New Connection"
		if f.editing != nil {
			title = "Edit " + f.editing.Config.Name
		}
		ui.Row(c).Padding(16, 20, 8, 20).Children(func() {
			ui.Text(c, title).FontSize(16).Bold()
		})
		pages := f.pages(engine)
		if f.page >= len(pages) {
			f.page = pageGeneral
		}
		labels := make([]string, len(pages))
		for i, pg := range pages {
			labels[i] = f.pageLabel(pg)
		}
		ui.Row(c).Padding(0, 20).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Tabs(c, &f.page, labels...).Label("Settings")
		})
		ui.Scroll(c).Grow(1).Shrink(1).Children(func() {
			ui.Form(c, func() {
				switch f.page {
				case pageGeneral:
					a.generalPage(c, f, engine)
				case pageNetwork:
					a.networkPage(c, f)
				case pageOptions:
					optionsPage(c, f, engine)
				}
			}).Padding(12, 20, 12, 20)
		})
		ui.Row(c).Padding(12, 20).Gap(8).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
			if ui.Button(c, "Test Connection").Disabled(f.testing).Clicked() {
				a.testConnection(f)
			}
			if f.testing {
				ui.Spinner(c).Size(14, 14)
			} else if f.testResult != "" {
				col := t.Danger
				if f.testOK {
					col = t.Success
				}
				ui.Text(c, widgets.FirstLine(f.testResult)).TextColor(col).SingleLine().Shrink(1).Tooltip(f.testResult)
			}
			ui.Spacer(c)
			if ui.Button(c, "Cancel").Clicked() {
				f.open = false
			}
			if ui.Button(c, "Save").Clicked() {
				a.saveConnForm(f, false)
			}
			if ui.PrimaryButton(c, "Save and Connect").Clicked() {
				a.saveConnForm(f, true)
			}
		})
		if f.err != "" {
			ui.Text(c, f.err).TextColor(t.Danger).Padding(0, 20, 12, 20)
		}
	})
}

// The pages of the connection form, in order: what most connections need
// comes first, and the rest on pages named for what they hold. The
// network is last, so that a file, which has none, keeps the others'
// places.
const (
	pageGeneral = iota
	pageOptions
	pageNetwork
)

// pages are the form's pages for an engine.
func (f *connForm) pages(e db.Engine) []int {
	if e.IsFile() {
		return []int{pageGeneral, pageOptions}
	}
	return []int{pageGeneral, pageOptions, pageNetwork}
}

// pageLabel names a page, marked when it holds a choice other than the
// default, so that it is not missed while another page shows.
func (f *connForm) pageLabel(page int) string {
	switch page {
	case pageNetwork:
		if f.cfg.SSH.Enabled || f.proxy != 0 || f.cfg.CertFile != "" {
			return "Network •"
		}
		return "Network"
	case pageOptions:
		if f.cfg.AutoConnect || f.commit != commitLabels[0] || f.idleNever || f.idleMin > 0 || f.timeout > 0 {
			return "Options •"
		}
		return "Options"
	}
	return "General"
}

// generalPage holds what a connection needs: where the database is, who
// connects, and how carefully.
func (a *App) generalPage(c *ui.Context, f *connForm, engine db.Engine) {
	t := c.Theme()
	pal := widgets.PaletteOf(c)
	ui.Field(c, "From URL", func() {
		in := ui.TextInput(c, &f.url).Font(widgets.MonoFont).FontSize(12.5).
			Placeholder("Paste postgres://user:password@host:5432/db, mysql://…, redis://…, or a file path")
		if f.urlFocus && in.Focus().Focused() {
			f.urlFocus = false
		}
		if in.Submitted() || in.Changed() && strings.Contains(f.url, "://") {
			a.fillFromURL(f)
		}
	}).Error(f.urlErr)
	ui.Field(c, "Engine", func() {
		if ui.Segmented(c, &f.engineIdx, engineLabels()...).Label("Engine").Changed() {
			a.syncEngineChoice(f, f.engineIdx)
		}
	})
	ui.Field(c, "Name", func() {
		name := ui.TextInput(c, &f.cfg.Name).Placeholder("e.g. Billing (production)")
		if !f.urlFocus {
			name.AutoFocus()
		}
	})
	if engine.IsFile() {
		ui.Field(c, "File", func() {
			ui.Row(c).Gap(6).Grow(1).Children(func() {
				ui.TextInput(c, &f.cfg.Database).Placeholder("/path/to/database").Grow(1)
				if ui.Button(c, "Choose…").Clicked() {
					a.chooseFile(engine)
				}
				if ui.Button(c, "New…").Tooltip("Create an empty " + engine.Label() + " database").Clicked() {
					a.newDatabaseFile(engine)
				}
			})
		}).Error(f.fileError(engine))
	} else {
		mode := db.RedisStandalone
		if engine == db.Redis {
			ui.Field(c, "Topology", func() {
				ui.Segmented(c, &f.redisMode, redisModeLabels...).Label("Topology")
			})
			mode = redisModes[f.redisMode]
		}
		hostLabel, port, more := "Host", engine.DefaultPort(), ""
		switch mode {
		case db.RedisCluster:
			hostLabel, more = "A node", "More nodes"
		case db.RedisSentinel:
			hostLabel, port, more = "A sentinel", db.SentinelPort, "More sentinels"
		}
		ui.Field(c, hostLabel, func() {
			ui.Row(c).Gap(6).Grow(1).Children(func() {
				ui.TextInput(c, &f.cfg.Host).Placeholder("localhost").Grow(1).Label(hostLabel)
				ui.TextInput(c, &f.port).Placeholder(strconv.Itoa(port)).Width(80).Label("Port")
			})
		})
		if more != "" {
			ui.Field(c, more, func() {
				ui.TextInput(c, &f.cfg.Redis.Nodes).Placeholder("10.0.0.2:" + strconv.Itoa(port) + ", 10.0.0.3:" + strconv.Itoa(port)).Font(widgets.MonoFont).FontSize(12.5)
			}).Description("Any one that answers is enough; more keep the connection working when one is down.")
		}
		if mode == db.RedisSentinel {
			ui.Field(c, "Master name", func() {
				ui.TextInput(c, &f.cfg.Redis.Master).Placeholder("mymaster")
			}).Description("The master the sentinels watch: the connection follows it to its replacement after a failover.")
		}
		ui.Field(c, "User", func() {
			ui.TextInput(c, &f.cfg.User).Placeholder(engine.DefaultUser())
		})
		ui.Field(c, "Password from", func() {
			ui.Select(c, &f.source, passwordSources).Label("Password from")
		})
		a.passwordField(c, f)
		label := "Database"
		placeholder := ""
		switch engine {
		case db.Redis:
			label, placeholder = "Database index", "0"
		case db.Postgres:
			placeholder = "postgres"
		}
		if mode != db.RedisCluster { // a cluster has database 0 only
			ui.Field(c, label, func() {
				ui.TextInput(c, &f.cfg.Database).Placeholder(placeholder)
			})
		}
		if mode == db.RedisSentinel {
			a.sentinelFields(c, f)
		}
	}
	ui.Field(c, "Environment", func() {
		ui.Column(c).Gap(6).Children(func() {
			seg := ui.SegmentedBase(c, &f.env, 3)
			seg.Track.Gap(4).Children(func() {
				for i, e := range db.Environments() {
					s := seg.Segment(i).Padding(5, 12).Radius(6).Border(1, t.Border)
					col := widgets.EnvironmentColor(e)
					if i == f.env {
						s.Background(col).Border(1, col)
					}
					s.Children(func() {
						txt := ui.Text(c, e.Label())
						if i == f.env {
							txt.TextColor(ui.RGB(255, 255, 255)).Bold()
						}
					})
				}
			})
			ui.Text(c, envHelp(db.Environments()[f.env])).FontSize(12).TextColor(pal.Muted)
		})
	})
	colorField(c, f)
	ui.Field(c, "Safety", func() {
		ui.Checkbox(c, &f.cfg.ReadOnly, "Read-only: refuse every write (the server enforces it too)")
	})
	ui.Field(c, "Project", func() {
		if f.editing != nil {
			ui.Text(c, a.projectLabel(f.project)).Tooltip(f.project.Dir)
			return
		}
		if ui.Select(c, &f.projectSel, a.projectLabels()).Label("Project").Changed() {
			f.project = a.projectByLabel(f.projectSel)
		}
	}).Description("Saved in its " + project.File + ", without passwords, to commit with the project.")
}

// connectionColors are the colors offered for a connection, apart from
// its environment's. None is an environment's: a development connection
// in production's red would cry wolf, and production in green would hide.
var connectionColors = []struct{ name, hex string }{
	{"Blue", "#2563eb"}, {"Violet", "#7c3aed"}, {"Pink", "#db2777"}, {"Orange", "#ea580c"},
	{"Yellow", "#ca8a04"}, {"Lime", "#65a30d"}, {"Teal", "#0d9488"}, {"Cyan", "#0891b2"}, {"Slate", "#64748b"},
}

// colorField chooses the connection's color: its environment's, one of
// connectionColors, or any other.
func colorField(c *ui.Context, f *connForm) {
	t := c.Theme()
	env := db.Environments()[f.env]
	swatch := func(value string, col ui.Color, name string) {
		on := f.cfg.Color == value
		r := ui.RadioBase(c, &f.cfg.Color, value).Size(22, 22).Radius(11).Center().Background(col).Label(name).Tooltip(name)
		if on {
			r.Border(2, t.Text)
		}
	}
	ui.Field(c, "Color", func() {
		ui.Row(c).Gap(8).Children(func() {
			ui.RadioGroup(c, func() {
				swatch("", widgets.EnvironmentColor(env), "The environment's ("+env.Label()+")")
				for _, cc := range connectionColors {
					swatch(cc.hex, ui.Hex(cc.hex), cc.name)
				}
			}).Row().Gap(6).Label("Color")
			custom := widgets.EnvColor(&db.Config{Color: f.cfg.Color, Env: env})
			if ui.ColorWell(c, &custom).Label("Other color").Tooltip("Other color").Changed() {
				f.cfg.Color = widgets.HexColor(custom)
			}
		})
	}).Description("Marks the connection's tabs, the band above them and the status bar. The status bar names the environment whatever the color.")
}

// networkPage holds how the app reaches a server: TLS and an SSH tunnel.
func (a *App) networkPage(c *ui.Context, f *connForm) {
	ui.Field(c, "TLS", func() {
		opts := []string{tlsLabels[db.TLSDisable], tlsLabels[db.TLSPrefer], tlsLabels[db.TLSRequire], tlsLabels[db.TLSVerifyFull]}
		ui.Select(c, &f.tls, opts).Label("TLS")
	})
	if f.tls == tlsLabels[db.TLSVerifyFull] {
		ui.Field(c, "CA certificate", func() {
			a.fileField(c, &f.cfg.CAFile, "System certificates", "Choose the CA Certificate")
		})
	}
	if f.tls != tlsLabels[db.TLSDisable] {
		ui.Field(c, "Client certificate", func() {
			a.fileField(c, &f.cfg.CertFile, "none", "Choose the Client Certificate")
		}).Description("A PEM certificate the server may ask the connection to log in with.")
		if f.cfg.CertFile != "" {
			ui.Field(c, "Client key", func() {
				a.fileField(c, &f.cfg.KeyFile, "the certificate's key, in PEM", "Choose the Client Key")
			})
		}
	}
	a.proxyFields(c, f)
	ui.Field(c, "SSH tunnel", func() {
		ui.Checkbox(c, &f.cfg.SSH.Enabled, "Connect through an SSH server")
	})
	if !f.cfg.SSH.Enabled {
		return
	}
	ui.Field(c, "SSH host", func() {
		ui.Row(c).Gap(6).Grow(1).Children(func() {
			ui.TextInput(c, &f.cfg.SSH.Host).Placeholder("bastion.example.com").Grow(1)
			ui.TextInput(c, &f.sshPort).Placeholder("22").Width(70).Label("SSH port")
		})
	})
	ui.Field(c, "SSH user", func() {
		ui.TextInput(c, &f.cfg.SSH.User).Placeholder("ubuntu")
	})
	ui.Field(c, "Jump hosts", func() {
		ui.TextInput(c, &f.cfg.SSH.Jump).Placeholder("none, or user@jump.example.com, jump2:2222").Grow(1)
	}).Description("SSH servers reached in turn before the SSH host, as ssh -J does; each logs in as the SSH host does, and its host key is checked.")
	ui.Field(c, "", func() {
		ui.Checkbox(c, &f.cfg.SSH.UseAgent, "Use the SSH agent")
	})
	ui.Field(c, "Private key", func() {
		a.fileField(c, &f.cfg.SSH.KeyPath, "~/.ssh/id_ed25519", "Choose a Private Key")
	})
	a.sshSecretFields(c, f)
}

// optionsPage holds how the app behaves with the connection: when it
// connects, commits, and gives up on idle transactions and slow
// statements.
func optionsPage(c *ui.Context, f *connForm, engine db.Engine) {
	pal := widgets.PaletteOf(c)
	ui.Field(c, "Auto-connect", func() {
		ui.Checkbox(c, &f.cfg.AutoConnect, "Connect as DGopher starts, and when its editors are shown")
	}).Description("Off, the connection opens when you open it, or run a statement in one of its editors.")
	if engine != db.ClickHouse && engine != db.Redis {
		ui.Field(c, "Commit", func() {
			ui.Select(c, &f.commit, commitLabels).Label("Commit mode")
		})
		ui.Field(c, "Idle transactions", func() {
			ui.Row(c).Gap(8).Children(func() {
				if !f.idleNever {
					ui.NumberInput(c, &f.idleMin, 0, 1440, 5).Label("Minutes")
					ui.Text(c, "minutes").TextColor(pal.Muted)
				}
				ui.Checkbox(c, &f.idleNever, "Never roll back")
			})
		}).Description(fmt.Sprintf("A transaction left idle this long is rolled back, after a warning you can cancel. 0 takes the environment's default: %d minutes on %s.",
			int(connection.DefaultIdleTx(db.Environments()[f.env]).Minutes()), db.Environments()[f.env].Label()))
	}
	if engine != db.Redis {
		ui.Field(c, "Statement timeout", func() {
			ui.Row(c).Gap(8).Children(func() {
				ui.NumberInput(c, &f.timeout, 0, 86400, 30).Label("Seconds")
				ui.Text(c, "seconds, 0 for none").TextColor(pal.Muted)
			})
		})
	}
}

func (f *connForm) fileError(e db.Engine) string {
	p := strings.TrimSpace(f.cfg.Database)
	if p == "" || p == ":memory:" {
		return ""
	}
	if _, err := os.Stat(db.ExpandPath(p)); err != nil {
		return "No such file"
	}
	return ""
}

func envHelp(e db.Environment) string {
	switch e {
	case db.Production:
		return "Every write and schema change asks first; destructive statements need the connection's name typed. Manual commit by default."
	case db.Staging:
		return "Schema changes and destructive statements ask first."
	}
	return "Destructive statements (DROP, TRUNCATE, DELETE without WHERE) ask first."
}

func engineLabels() []string {
	var out []string
	for _, e := range db.Engines() {
		out = append(out, e.Label())
	}
	return out
}

// syncEngineChoice follows a new engine with the defaults of host, port
// and user that the user has not changed.
func (a *App) syncEngineChoice(f *connForm, idx int) {
	e := db.Engines()[idx]
	if e.Label() == f.engine {
		return
	}
	old := engineByLabel(f.engine)
	f.engine = e.Label()
	if f.cfg.User == "" || f.cfg.User == old.DefaultUser() {
		f.cfg.User = e.DefaultUser()
	}
	if f.port == strconv.Itoa(old.DefaultPort()) {
		f.port = ""
	}
	if e.IsFile() && !old.IsFile() {
		f.cfg.Database = ""
	}
}

func (a *App) chooseFile(e db.Engine) {
	exts := []string{"sqlite", "sqlite3", "db", "db3"}
	if e == db.DuckDB {
		exts = []string{"duckdb", "ddb", "db"}
	}
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Title:   "Open " + e.Label() + " Database",
			Filters: []mygo.FileFilter{{Name: e.Label() + " databases", Extensions: exts}, {Name: "All files", Extensions: []string{"*"}}},
		})
		if err != nil || len(paths) == 0 {
			return
		}
		a.Post(func() {
			if a.connForm != nil {
				a.connForm.cfg.Database = paths[0]
			}
		})
	}()
}

// newDatabaseFile asks where to make an empty database of a file engine,
// makes it there, and puts it in the form. A file chosen that exists is
// opened as it is.
func (a *App) newDatabaseFile(e db.Engine) {
	name := "database.sqlite"
	if e == db.DuckDB {
		name = "database.duckdb"
	}
	go func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: "New " + e.Label() + " Database", DefaultPath: name})
		if err != nil || path == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := db.CreateFile(ctx, e, path); err != nil {
			a.Post(func() { a.ShowError("Could not create the database", err.Error()) })
			return
		}
		a.Post(func() {
			if a.connForm != nil {
				a.connForm.cfg.Database = path
			}
		})
	}()
}

// fileField is a path typed, or chosen with a button: the dialog starts
// in the folder of the path there, else in ~/.ssh, and shows hidden files.
func (a *App) fileField(c *ui.Context, path *string, placeholder, title string) {
	ui.Row(c).Gap(6).Grow(1).Children(func() {
		ui.TextInput(c, path).Placeholder(placeholder).Grow(1).Label(title)
		if ui.Button(c, "Choose…").Clicked() {
			form, start := a.connForm, filepath.Dir(db.ExpandPath(*path))
			if *path == "" {
				home, _ := os.UserHomeDir()
				start = filepath.Join(home, ".ssh")
			}
			go func() {
				paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: title, DefaultPath: start, ShowHiddenFiles: true})
				if err != nil || len(paths) == 0 {
					return
				}
				a.Post(func() {
					if a.connForm == form {
						*path = paths[0] // the form's field, while the form is open
					}
				})
			}()
		}
	})
}

func (a *App) testConnection(f *connForm) {
	cfg := f.config()
	if cfg.ID == "" {
		cfg.ID = "test"
	}
	if err := a.checkConnForm(f, &cfg); err != nil {
		f.testResult, f.testOK = err.Error(), false
		return
	}
	if f.editing != nil && !a.trusted(f.editing) && !reconnectNeeded(f.loaded, cfg) {
		// Its destination and commands came in the file, not from this
		// form: they need the same agreement as a connect.
		a.AskConfirm(f.editing, safety.Verdict{Reasons: []string{sharedDestination(&cfg, f.project.Name)}},
			"Test "+cfg.Name+"?", "Test", "", func() {
				a.trust(f.editing)
				a.testConnection(f)
			})
		return
	}
	if cfg.Password == "" && cfg.PasswordEnv != "" {
		cfg.Password = os.Getenv(cfg.PasswordEnv)
	}
	if cfg.AskPassword && !cfg.Engine.IsFile() {
		// Asked for this test only: it is not kept.
		a.askPassword(&passwordPrompt{open: true, name: cfg.Name, where: cfg.User + "@" + cfg.Host, action: "Test",
			onSubmit: func(pw string) {
				cfg.Password = pw
				a.runTest(f, cfg)
			}, onCancel: func() {}})
		return
	}
	a.runTest(f, cfg)
}

// runTest connects with the form's settings, and says how it went.
func (a *App) runTest(f *connForm, cfg db.Config) {
	f.testing, f.testResult = true, ""
	known := a.knownHosts()
	a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second+secretcmd.Timeout)
		defer cancel()
		start := time.Now()
		var version string
		err := resolveCommandSecrets(ctx, &cfg)
		switch {
		case err != nil:
		case cfg.Engine == db.Redis:
			var kv *db.KV
			if kv, err = db.OpenRedis(ctx, cfg, known); err == nil {
				version = "Redis " + kv.ServerVersion(ctx)
				kv.Close()
			}
		default:
			var d *db.DB
			if d, err = db.Open(ctx, cfg, known); err == nil {
				version = cfg.Engine.Label() + " " + d.ServerVersion(ctx)
				d.Close()
			}
		}
		elapsed := time.Since(start).Round(time.Millisecond)
		return func() {
			f.testing = false
			if err != nil {
				f.testResult, f.testOK = err.Error(), false
				return
			}
			f.testResult, f.testOK = "Connected to "+version+" in "+elapsed.String(), true
		}
	})
}

// checkConnForm says what keeps the form's connection from being saved.
func (a *App) checkConnForm(f *connForm, cfg *db.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if f.project == nil || f.project.Err != "" {
		return errors.New("choose the project to keep the connection in")
	}
	if why := envAllowed(cfg); why != "" {
		return errors.New(why)
	}
	// Only the form refuses a malformed color: one from a shared file is
	// drawn as the environment's, and does not stop a connect.
	if cfg.Color != "" && !db.ValidColor(cfg.Color) {
		return fmt.Errorf("the color %q is not #rrggbb: choose one", cfg.Color)
	}
	if sourceOf(cfg) == sourceEnv && cfg.PasswordEnv == "" {
		return errors.New("name the environment variable holding the password")
	}
	for _, cmd := range []string{cfg.PasswordCommand, cfg.SSH.PasswordCommand} {
		if cmd == "" {
			continue
		}
		if _, err := secretcmd.Split(cmd); err != nil {
			return err
		}
	}
	if sourceOf(cfg) == sourceCommand && cfg.PasswordCommand == "" {
		return errors.New("type the command that prints the password")
	}
	if cfg.Redis.SentinelPassword != "" && !a.st.Secrets().Available() {
		return errors.New("no system keychain is available to keep the sentinels' password")
	}
	if cfg.Proxy.Password != "" && !a.st.Secrets().Available() {
		return errors.New("no system keychain is available to keep the proxy's password")
	}
	return nil
}

func (a *App) saveConnForm(f *connForm, connect bool) {
	cfg := f.config()
	if err := a.checkConnForm(f, &cfg); err != nil {
		f.err = err.Error()
		return
	}
	if !a.st.Secrets().Available() && !cfg.Engine.IsFile() && cfg.Password != "" {
		cfg.AskPassword = true
	}
	cn := f.editing
	if cn != nil && cn.Status != connection.StatusIdle && reconnectNeeded(f.loaded, cfg) {
		// The open connection no longer matches: its tabs close with it.
		a.requestDisconnect("Apply the changes to "+cn.Config.Name+"?", []*connection.Conn{cn}, func() {
			a.disconnect(cn)
			a.finishConnForm(f, cfg, connect)
		})
		return
	}
	a.finishConnForm(f, cfg, connect)
}

// reconnectNeeded reports whether an edit changes how the app reaches the
// database, rather than only its name, color or safety prompts.
func reconnectNeeded(old, cfg db.Config) bool {
	strip := func(c db.Config) db.Config {
		c.Name, c.Color, c.Env, c.Commit = "", "", "", ""
		c.StatementTimeout, c.IdleTxTimeout = 0, 0
		return c
	}
	return strip(old) != strip(cfg)
}

func (a *App) finishConnForm(f *connForm, cfg db.Config, connect bool) {
	if err := f.project.Writable(); err != nil {
		f.err = "Could not save " + project.File + ": " + err.Error()
		return
	}
	cn := f.editing
	if cn == nil {
		cfg.ID = a.uniqueConnID(f.project, cfg.Name)
	}
	if err := a.saveSecrets(&cfg, !cfg.AskPassword); err != nil {
		f.err = "Could not save the password in the keychain: " + err.Error()
		return
	}
	// Secrets are kept for the host they were given for: those of the
	// old destination go, once the new ones are safe.
	if f.editing != nil {
		for _, what := range secretNames {
			if secretKey(&f.loaded, what) != secretKey(&cfg, what) {
				a.st.Secrets().Delete(secretKey(&f.loaded, what))
			}
		}
	}
	password := cfg.Password
	cfg.Password, cfg.SSH.Password, cfg.SSH.KeyPassphrase, cfg.Redis.SentinelPassword, cfg.Proxy.Password = "", "", "", "", ""
	if cn == nil {
		cn = &connection.Conn{Project: f.project}
		cn.Reset()
		a.conns = append(a.conns, cn)
	}
	untrusted := f.editing != nil && !a.trusted(f.editing)
	cn.Config = cfg
	// Typed here: the user chose where it goes and what it runs. A
	// connection from the file that is only renamed is not.
	if !untrusted || reconnectNeeded(f.loaded, cfg) {
		a.trust(cn)
	}
	a.saveProject(cn.Project)
	verb := "changed"
	if f.editing == nil {
		verb = "added"
	}
	a.Record(&cfg, audit.Event{Kind: audit.KindConfig, Detail: "connection " + verb + ": " + cfg.Env.Label() + ", " + connectionSummary(&cfg)})
	f.open = false
	if connect {
		if cfg.AskPassword && password != "" {
			// Typed just now: use it once without keeping it.
			c := cfg
			c.Password = password
			a.open(cn, c)
			a.nav.expand(navNode{kind: nodeConn, conn: cn.Config.ID})
			return
		}
		a.activate(navNode{kind: nodeConn, conn: cn.Config.ID})
	}
}

// fillFromURL fills the form from a pasted URL, then forgets the URL,
// which may hold a password.
func (a *App) fillFromURL(f *connForm) {
	cfg, err := db.ParseURL(f.url)
	if err != nil {
		f.urlErr = err.Error()
		return
	}
	f.urlErr, f.url = "", ""
	keepName := f.cfg.Name != ""
	name := f.cfg.Name
	f.cfg.Engine, f.cfg.Host, f.cfg.User, f.cfg.Database = cfg.Engine, cfg.Host, cfg.User, cfg.Database
	if cfg.Password != "" {
		f.cfg.Password = cfg.Password
	}
	if keepName {
		f.cfg.Name = name
	} else {
		f.cfg.Name = cfg.Name
	}
	f.engine = cfg.Engine.Label()
	for i, e := range db.Engines() {
		if e == cfg.Engine {
			f.engineIdx = i
		}
	}
	f.port = ""
	if cfg.Port > 0 {
		f.port = strconv.Itoa(cfg.Port)
	}
	if cfg.TLS != "" {
		f.tls = tlsLabels[cfg.TLS]
	}
}

// passwordSource is where a connection's password comes from.
type passwordSource int

const (
	sourceKeychain passwordSource = iota
	sourceEnv
	sourceCommand
	sourceAsk
)

var passwordSources = []string{"System keychain", "Environment variable", "Command", "Ask every time"}
var sshSources = []string{"System keychain", "Command"}

func sourceOf(cfg *db.Config) passwordSource {
	switch {
	case cfg.AskPassword:
		return sourceAsk
	case cfg.PasswordCommand != "":
		return sourceCommand
	case cfg.PasswordEnv != "":
		return sourceEnv
	}
	return sourceKeychain
}

func sourceByLabel(label string) passwordSource {
	for i, l := range passwordSources {
		if l == label {
			return passwordSource(i)
		}
	}
	return sourceKeychain
}

// keychainName names the system keychain the go-keyring library uses on
// this system.
func keychainName() string {
	switch runtime.GOOS {
	case "darwin":
		return "the macOS Keychain"
	case "windows":
		return "the Windows Credential Manager"
	}
	return "the Secret Service (GNOME Keyring or KWallet)"
}

// keychainNote says where secrets go in the keychain, and how they are
// kept, by their accounts there.
func keychainNote(accounts ...string) string {
	quoted := make([]string, len(accounts))
	for i, acc := range accounts {
		quoted[i] = strconv.Quote(acc)
	}
	what := "account " + quoted[0]
	if len(quoted) > 1 {
		what = "accounts " + strings.Join(quoted, " and ")
	}
	return "Saved in " + keychainName() + " under the service " + strconv.Quote(keychainService) + ", " + what +
		". The keychain encrypts it with your login; it is never written to " + project.File + " or any other file."
}

// storageNote says where, and how, the form's password would be kept.
func (a *App) storageNote(f *connForm, cfg *db.Config) string {
	switch sourceByLabel(f.source) {
	case sourceEnv:
		name := "the variable above"
		if cfg.PasswordEnv != "" {
			name = "$" + cfg.PasswordEnv
		}
		return "Not stored. Read from " + name + " each time you connect."
	case sourceCommand:
		return "Not stored. The command runs each time you connect, without a shell; on each computer you approve it once, and again whenever it changes. What it prints is never logged or saved."
	case sourceAsk:
		return "Not stored. Asked for on each connect, and kept in memory only until you disconnect."
	}
	if !a.st.Secrets().Available() {
		return "Not saved: no system keychain is available on this computer, so it is asked for on each connect."
	}
	if cfg.Password == "" {
		return "Nothing to save: no password is typed."
	}
	return keychainNote(secretKey(a.formID(f, cfg), "password"))
}

// formID is the connection with the ID it has, or will have once saved.
func (a *App) formID(f *connForm, cfg *db.Config) *db.Config {
	c := *cfg
	if f.editing == nil && f.project != nil {
		c.ID = a.uniqueConnID(f.project, c.Name)
	}
	return &c
}

// passwordField shows the field of the chosen password source, and where
// the password would be kept.
func (a *App) passwordField(c *ui.Context, f *connForm) {
	cfg := f.config()
	note := a.storageNote(f, &cfg)
	switch sourceByLabel(f.source) {
	case sourceKeychain:
		ui.Field(c, "Password", func() {
			ui.TextInput(c, &f.cfg.Password).Password()
		}).Description(note)
	case sourceEnv:
		ui.Field(c, "Variable", func() {
			ui.TextInput(c, &f.cfg.PasswordEnv).Placeholder("DGOPHER_BILLING_PASSWORD").Font(widgets.MonoFont).FontSize(12.5)
		}).Description(note).Error(envAllowed(&cfg))
	case sourceCommand:
		var bad string
		if cfg.PasswordCommand != "" {
			if _, err := secretcmd.Split(cfg.PasswordCommand); err != nil {
				bad = err.Error()
			}
		}
		ui.Field(c, "Command", func() {
			ui.TextInput(c, &f.cfg.PasswordCommand).Placeholder(`op read "op://Vault/Billing DB/password"`).Font(widgets.MonoFont).FontSize(12.5)
		}).Description(note).Error(bad)
	case sourceAsk:
		ui.Field(c, "", func() {
			ui.Text(c, note).FontSize(12).TextColor(widgets.PaletteOf(c).Muted)
		})
	}
}

// proxyKinds are the proxy choices of the form, as proxyLabels names them.
var (
	proxyKinds  = []string{"", netproxy.SOCKS5, netproxy.HTTP}
	proxyLabels = []string{"None", "SOCKS5", "HTTP (CONNECT)"}
)

// proxyFields show the proxy the connection goes through, and how it
// logs in to it.
func (a *App) proxyFields(c *ui.Context, f *connForm) {
	ui.Field(c, "Proxy", func() {
		ui.Segmented(c, &f.proxy, proxyLabels...).Label("Proxy")
	}).Description("The connection goes through it to the server, or to the SSH host when there is one.")
	if f.proxy == 0 {
		return
	}
	ui.Field(c, "Proxy host", func() {
		ui.Row(c).Gap(6).Grow(1).Children(func() {
			ui.TextInput(c, &f.cfg.Proxy.Host).Placeholder("proxy.example.com").Grow(1).Label("Proxy host")
			ui.TextInput(c, &f.proxyPort).Placeholder("1080").Width(70).Label("Proxy port")
		})
	})
	ui.Field(c, "Proxy user", func() {
		ui.TextInput(c, &f.cfg.Proxy.User).Placeholder("none").Label("Proxy user")
	})
	note := "Not needed when the proxy asks for no password."
	switch {
	case f.cfg.Proxy.Password == "":
	case !a.st.Secrets().Available():
		note = "No system keychain is available on this computer to keep it."
	default:
		note = keychainNote(secretKey(a.formID(f, &f.cfg), "proxy-password"))
	}
	ui.Field(c, "Proxy password", func() {
		ui.TextInput(c, &f.cfg.Proxy.Password).Password().Placeholder("If the proxy asks for one").Label("Proxy password")
	}).Description(note)
}

// sentinelFields show how the connection logs in to the sentinels, which
// keep credentials of their own, and where their password would be kept.
func (a *App) sentinelFields(c *ui.Context, f *connForm) {
	ui.Field(c, "Sentinel user", func() {
		ui.TextInput(c, &f.cfg.Redis.SentinelUser).Placeholder("none")
	})
	note := "Not needed when the sentinels ask for no password."
	switch {
	case f.cfg.Redis.SentinelPassword == "":
	case !a.st.Secrets().Available():
		note = "No system keychain is available on this computer to keep it."
	default:
		note = keychainNote(secretKey(a.formID(f, &f.cfg), "sentinel-password"))
	}
	ui.Field(c, "Sentinel password", func() {
		ui.TextInput(c, &f.cfg.Redis.SentinelPassword).Password().Placeholder("If the sentinels ask for one")
	}).Description(note)
}

// sshSecretFields shows the SSH password and passphrase, or the command
// that prints them, and where they would be kept.
func (a *App) sshSecretFields(c *ui.Context, f *connForm) {
	ui.Field(c, "SSH secret from", func() {
		ui.Select(c, &f.sshSource, sshSources).Label("SSH secret from")
	})
	if f.sshSource == sshSources[1] {
		ui.Field(c, "SSH command", func() {
			ui.TextInput(c, &f.cfg.SSH.PasswordCommand).Placeholder(`op read "op://Vault/Bastion/password"`).Font(widgets.MonoFont).FontSize(12.5)
		}).Description("Prints the key's passphrase when a key is set, else the SSH password. Not stored: it runs each time you connect, without a shell, once you approve it on this computer.")
		return
	}
	cfg := a.formID(f, &f.cfg)
	ui.Field(c, "Passphrase", func() {
		ui.TextInput(c, &f.cfg.SSH.KeyPassphrase).Password().Placeholder("If the key has one")
	})
	ui.Field(c, "SSH password", func() {
		ui.TextInput(c, &f.cfg.SSH.Password).Password().Placeholder("If no key is used")
	})
	var accounts []string
	if f.cfg.SSH.KeyPassphrase != "" {
		accounts = append(accounts, secretKey(cfg, "ssh-passphrase"))
	}
	if f.cfg.SSH.Password != "" {
		accounts = append(accounts, secretKey(cfg, "ssh-password"))
	}
	note := "Nothing to save: no passphrase or SSH password is typed."
	switch {
	case !a.st.Secrets().Available():
		note = "Not saved: no system keychain is available on this computer."
	case len(accounts) > 0:
		note = keychainNote(accounts...)
	}
	ui.Field(c, "", func() {
		ui.Text(c, note).FontSize(12).TextColor(widgets.PaletteOf(c).Muted).Selectable()
	})
}

// usableProjects are the projects a connection may be added to.
func (a *App) usableProjects() []*project.Project {
	var out []*project.Project
	for _, p := range a.projects {
		if p.Err == "" {
			out = append(out, p)
		}
	}
	return out
}

// projectLabel names a project in a choice; folders of the same name
// show where they are.
func (a *App) projectLabel(p *project.Project) string {
	if p == nil {
		return ""
	}
	for _, q := range a.projects {
		if q != p && q.Name == p.Name {
			return p.Name + " (" + filepath.Dir(p.Dir) + ")"
		}
	}
	return p.Name
}

func (a *App) projectLabels() []string {
	var out []string
	for _, p := range a.usableProjects() {
		out = append(out, a.projectLabel(p))
	}
	return out
}

func (a *App) projectByLabel(label string) *project.Project {
	for _, p := range a.usableProjects() {
		if a.projectLabel(p) == label {
			return p
		}
	}
	return nil
}
