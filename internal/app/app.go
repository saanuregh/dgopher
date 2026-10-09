// Package app is the DGopher window: the navigator, the tabs, and the
// dialogs that act across them.
package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/safety"
	"dgopher/internal/secretcmd"
	"dgopher/internal/settings"
	"dgopher/internal/sshtunnel"
	"dgopher/internal/store"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/redis"
	"dgopher/internal/ui/widgets"
)

// App is the state of the main window.
type App struct {
	win interface{ Invalidate() }
	// windowFocused reports whether the window has the keyboard focus;
	// notify shows a system notification, whose click runs onClick off
	// the main thread. Both are the window's, nil without one.
	windowFocused func() bool
	notify        func(title, body string, onClick func())
	st            *store.Store

	settings settings.Settings
	conns    []*connection.Conn
	tabs     []widgets.Tab
	active   int

	// Work finished on other goroutines, applied as the next frame starts.
	queueMu sync.Mutex
	queue   []func()

	nav      navState
	connForm *connForm
	data     dataview.Dialogs
	queries  query.Dialogs
	confirm  *widgets.ConfirmRequest
	// confirmQueue are the requests asked while another was shown.
	confirmQueue []*widgets.ConfirmRequest
	prompt       *passwordPrompt
	// promptQueue are the passwords asked while another was asked.
	promptQueue []*passwordPrompt
	hostKey     *hostKeyRequest
	alert       *alertRequest
	palette     *paletteState
	closing     *closeRequest
	pending     *pendingAsk
	importing   *importState
	sqlFile     *sqlFileRun
	projects    []*project.Project
	newProject  *newProjectForm
	renaming    *renameForm
	generating  *generateState
	quitting    bool // the user agreed to what quitting loses
	idleWarn    *idleWarning
	auditView   *auditState
	snippetForm *snippetForm
	history     *historyState
	toast       *pendingToast

	settingsOpen  bool
	shortcutsOpen bool
	// focusWant is where a key asked the keyboard focus to go: "nav",
	// "filter", "editor" or "results". The view that holds that place
	// takes it, and clears it once it has the focus.
	focusWant     string
	sidebarHidden bool
	settingsDirty bool
	clipboard     func(string)
	readClip      func() string

	// auditLogs are the projects' audit logs by the prefix of their
	// connections' IDs, for record, which may run on any goroutine.
	auditMu   sync.Mutex
	auditLogs map[string]*audit.Log

	appKnownHosts  string
	now            time.Time
	workspaceSaved time.Time
}

func newApp(st *store.Store) *App {
	a := &App{st: st, settings: settings.Default()}
	if err := st.LoadJSON("settings.json", &a.settings); err != nil && !errors.Is(err, store.ErrNotFound) {
		log.Println("settings:", err)
	}
	if a.settings.PageSize <= 0 {
		a.settings.PageSize = 500
	}
	if a.settings.EditorFont <= 0 {
		a.settings.EditorFont = 13
	}
	a.appKnownHosts = filepath.Join(st.Dir(), "known_hosts")
	a.auditLogs = map[string]*audit.Log{}
	a.nav.init()
	a.loadProjects()
	for _, p := range a.projects {
		if p.Err == "" {
			a.restoreWorkspace(p)
		}
	}
	for _, cn := range a.conns {
		if cn.Config.AutoConnect && cn.Project.Err == "" {
			a.Connect(cn, nil)
		}
	}
	return a
}

// Post runs fn on the main thread before the next frame.
func (a *App) Post(fn func()) {
	a.queueMu.Lock()
	a.queue = append(a.queue, fn)
	a.queueMu.Unlock()
	if a.win != nil {
		a.win.Invalidate()
	}
}

// drain applies the work posted since the last frame.
func (a *App) drain() {
	for {
		a.queueMu.Lock()
		q := a.queue
		a.queue = nil
		a.queueMu.Unlock()
		if len(q) == 0 {
			return
		}
		for _, fn := range q {
			fn()
		}
	}
}

// Background runs work on another goroutine; the function it returns
// runs on the main thread.
func (a *App) Background(work func() func()) {
	go func() {
		apply := work()
		if apply != nil {
			a.Post(apply)
		}
	}()
}

func (a *App) SaveSettings() {
	if err := a.st.SaveJSON("settings.json", a.settings); err != nil {
		log.Println("settings:", err)
	}
}

func (a *App) connByID(id string) *connection.Conn {
	for _, cn := range a.conns {
		if cn.Config.ID == id {
			return cn
		}
	}
	return nil
}

// Secrets are kept in the system keychain under the connection's ID and
// where they go: a password typed for one host is never sent to another,
// as when a shared project file changes the host of a connection.
func secretKey(cfg *db.Config, what string) string {
	// A cluster's nodes and the sentinels are where a password goes too.
	// They join only when set, for the keys of the passwords kept before
	// they existed to stay as they were.
	where := fmt.Sprintf("%s|%s|%d|%s|%s|%v|%s|%d|%s%s", cfg.Engine, cfg.Host, cfg.Port, cfg.User, cfg.Database,
		cfg.SSH.Enabled, cfg.SSH.Host, cfg.SSH.Port, cfg.SSH.User, redisWhere(cfg))
	if strings.HasPrefix(what, "ssh-") {
		where = fmt.Sprintf("ssh|%s|%d|%s|%s", cfg.SSH.Host, cfg.SSH.Port, cfg.SSH.User, cfg.SSH.KeyPath)
	}
	sum := sha256.Sum256([]byte(where))
	return "conn/" + cfg.ID + "/" + hex.EncodeToString(sum[:6]) + "/" + what
}

// redisWhere is where a Redis connection's passwords go besides its host,
// "" for one server.
func redisWhere(cfg *db.Config) string {
	if cfg.Redis == (db.RedisConfig{}) {
		return ""
	}
	return fmt.Sprintf("|redis|%s|%s|%s|%s", cfg.Redis.Mode, cfg.Redis.Nodes, cfg.Redis.Master, cfg.Redis.SentinelUser)
}

// clientCertWhere is the client certificate a connection logs in with,
// "" for none, which keeps the fingerprints of connections without one.
func clientCertWhere(cfg *db.Config) string {
	if cfg.CertFile == "" {
		return ""
	}
	return fmt.Sprintf("|cert|%q|%q", cfg.CertFile, cfg.KeyFile)
}

// jumpWhere is the SSH servers a connection goes through before its SSH
// host, "" for none, which keeps the fingerprints of connections without.
func jumpWhere(cfg *db.Config) string {
	if !cfg.SSH.Enabled || cfg.SSH.Jump == "" {
		return ""
	}
	return fmt.Sprintf("|jump|%q", cfg.SSH.Jump)
}

// proxyWhere is the proxy a connection goes through, "" for none, which
// keeps the fingerprints of connections without one.
func proxyWhere(cfg *db.Config) string {
	if cfg.Proxy.Kind == "" {
		return ""
	}
	p := cfg.Proxy
	return fmt.Sprintf("|proxy|%s|%s|%d|%s", p.Kind, p.Host, p.Port, p.User)
}

// projectEnvPrefix is what the environment variables named by a project
// file must start with: a cloned repository may not have the app send
// GITHUB_TOKEN, or any other variable, to a host of its choice.
const projectEnvPrefix = "DGOPHER_"

// envAllowed says why a connection may not read its password from its
// environment variable, "" when it may.
func envAllowed(cfg *db.Config) string {
	if cfg.PasswordEnv == "" || strings.HasPrefix(cfg.PasswordEnv, projectEnvPrefix) {
		return ""
	}
	return fmt.Sprintf("A connection may only read its password from a variable whose name starts with %s, not %s.", projectEnvPrefix, cfg.PasswordEnv)
}

func (a *App) loadSecrets(cfg *db.Config) {
	if cfg.Password == "" && cfg.PasswordEnv != "" && envAllowed(cfg) == "" {
		cfg.Password = os.Getenv(cfg.PasswordEnv)
	}
	sec := a.st.Secrets()
	if !sec.Available() {
		return
	}
	if cfg.Password == "" {
		cfg.Password, _ = sec.Get(secretKey(cfg, "password"))
	}
	if cfg.Redis.Mode == db.RedisSentinel && cfg.Redis.SentinelPassword == "" {
		cfg.Redis.SentinelPassword, _ = sec.Get(secretKey(cfg, "sentinel-password"))
	}
	if cfg.Proxy.Kind != "" && cfg.Proxy.Password == "" {
		cfg.Proxy.Password, _ = sec.Get(secretKey(cfg, "proxy-password"))
	}
	if cfg.SSH.Enabled {
		if cfg.SSH.Password == "" {
			cfg.SSH.Password, _ = sec.Get(secretKey(cfg, "ssh-password"))
		}
		if cfg.SSH.KeyPassphrase == "" {
			cfg.SSH.KeyPassphrase, _ = sec.Get(secretKey(cfg, "ssh-passphrase"))
		}
	}
}

func (a *App) saveSecrets(cfg *db.Config, savePassword bool) error {
	sec := a.st.Secrets()
	if !sec.Available() {
		return nil
	}
	set := func(what, v string) error {
		if v == "" || !savePassword {
			return sec.Delete(secretKey(cfg, what))
		}
		return sec.Set(secretKey(cfg, what), v)
	}
	return errors.Join(set("password", cfg.Password), set("ssh-password", cfg.SSH.Password), set("ssh-passphrase", cfg.SSH.KeyPassphrase),
		set("sentinel-password", cfg.Redis.SentinelPassword), set("proxy-password", cfg.Proxy.Password))
}

// secretNames are what a connection keeps in the keychain, as secretKey
// names them.
var secretNames = []string{"password", "ssh-password", "ssh-passphrase", "sentinel-password", "proxy-password"}

func (a *App) deleteSecrets(cfg *db.Config) {
	sec := a.st.Secrets()
	for _, what := range secretNames {
		sec.Delete(secretKey(cfg, what))
	}
}

func (a *App) knownHosts() []string {
	var files []string
	if home, err := os.UserHomeDir(); err == nil {
		files = append(files, filepath.Join(home, ".ssh", "known_hosts"))
	}
	return append(files, a.appKnownHosts)
}

// resolveCommandSecrets runs the commands that print a connection's
// secrets, for those not known yet. It runs off the main thread: a
// password manager may wait for a fingerprint.
func resolveCommandSecrets(ctx context.Context, cfg *db.Config) error {
	if cfg.PasswordCommand != "" && cfg.Password == "" {
		pw, err := secretcmd.Run(ctx, cfg.PasswordCommand)
		if err != nil {
			return fmt.Errorf("the password command failed: %w", err)
		}
		cfg.Password = pw
	}
	if cfg.SSH.Enabled && cfg.SSH.PasswordCommand != "" && cfg.SSH.Password == "" && cfg.SSH.KeyPassphrase == "" {
		pw, err := secretcmd.Run(ctx, cfg.SSH.PasswordCommand)
		if err != nil {
			return fmt.Errorf("the SSH command failed: %w", err)
		}
		// Never both: a key's passphrase is not for the server to see.
		if cfg.SSH.KeyPath != "" {
			cfg.SSH.KeyPassphrase = pw
		} else {
			cfg.SSH.Password = pw
		}
	}
	return nil
}

// Connect opens a connection, then runs then on the main thread.
func (a *App) Connect(cn *connection.Conn, then func()) {
	switch cn.Status {
	case connection.StatusConnected:
		if then != nil {
			then()
		}
		return
	case connection.StatusConnecting:
		if then != nil {
			cn.Waiters = append(cn.Waiters, then)
		}
		return
	}
	if then != nil {
		cn.Waiters = append(cn.Waiters, then)
	}
	cfg := cn.Config
	if !a.trusted(cn) {
		// A file from a repository chose this destination: the user says
		// whether a password may go there, once per destination.
		cn.Waiters = nil
		if a.asking(func(r *widgets.ConfirmRequest) bool { return r.Trust && r.Conn == cn }) {
			return // asking already
		}
		r := a.AskConfirm(cn, safety.Verdict{Reasons: []string{sharedDestination(&cfg, cn.Project.Name)}},
			"Connect to "+cfg.Name+"?", "Connect", "", func() {
				a.trust(cn)
				a.Connect(cn, then)
			})
		r.Trust = true
		r.OnCancel = func() {
			// Not idle: what connects as it shows, as a restored editor,
			// would ask again at once.
			cn.Status, cn.Err = connection.StatusFailed, "not connected: you did not agree to its destination"
		}
		return
	}
	if why := envAllowed(&cfg); why != "" {
		cn.Status, cn.Err = connection.StatusFailed, why
		cn.Waiters = nil
		a.ShowError("Could not connect to "+cfg.Name, why)
		return
	}
	cn.Status, cn.Err = connection.StatusConnecting, ""
	gen := cn.Generation
	a.Background(func() func() {
		a.loadSecrets(&cfg) // the keychain can take a while to answer
		return func() {
			if gen != cn.Generation {
				return // disconnected meanwhile
			}
			a.askPasswordOrOpen(cn, cfg)
		}
	})
}

func (a *App) askPasswordOrOpen(cn *connection.Conn, cfg db.Config) {
	if cfg.AskPassword && cfg.Password == "" && !cfg.Engine.IsFile() {
		if a.prompt != nil && a.prompt.conn == cn || slices.ContainsFunc(a.promptQueue, func(p *passwordPrompt) bool { return p.conn == cn }) {
			return // asking already
		}
		a.askPassword(&passwordPrompt{conn: cn, open: true, name: cfg.Name, where: cfg.User + "@" + cfg.Host, action: "Connect", onSubmit: func(pw string) {
			cfg.Password = pw
			a.open(cn, cfg)
		}, onCancel: func() {
			cn.Waiters = nil
			// Not idle: what connects as it shows would ask again at once.
			cn.Status, cn.Err = connection.StatusFailed, "no password was given"
		}})
		return
	}
	a.open(cn, cfg)
}

// askPassword shows a password prompt, after the one shown if any.
func (a *App) askPassword(p *passwordPrompt) {
	if a.prompt != nil {
		a.promptQueue = append(a.promptQueue, p)
		return
	}
	a.prompt = p
}

func (a *App) open(cn *connection.Conn, cfg db.Config) {
	cn.Status, cn.Err = connection.StatusConnecting, ""
	known := a.knownHosts()
	gen := cn.Generation
	a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second+secretcmd.Timeout)
		defer cancel()
		var sqldb *db.DB
		var kv *db.KV
		var version string
		err := resolveCommandSecrets(ctx, &cfg)
		switch {
		case err != nil:
		case cfg.Engine == db.Redis:
			kv, err = db.OpenRedis(ctx, cfg, known)
			if err == nil {
				version = kv.ServerVersion(ctx)
			}
		default:
			sqldb, err = db.Open(ctx, cfg, known)
			if err == nil {
				version = sqldb.ServerVersion(ctx)
			}
		}
		return func() {
			if gen != cn.Generation {
				// Disconnected, or edited, meanwhile: this pool is not the
				// connection's any more.
				if sqldb != nil {
					go sqldb.Close()
				}
				if kv != nil {
					go kv.Close()
				}
				return
			}
			ev := audit.Event{Kind: audit.KindConnect, Detail: connectionSummary(&cfg)}
			if err != nil {
				ev.Error = err.Error()
			} else if version != "" {
				ev.Detail += ", server " + widgets.FirstLine(version)
			}
			a.Record(&cfg, ev)
			if err != nil {
				cn.Status, cn.Err = connection.StatusFailed, err.Error()
				cn.Waiters = nil
				var hk *sshtunnel.HostKeyError
				if errors.As(err, &hk) {
					a.askHostKey(cn, cfg, hk)
					return
				}
				a.ShowError("Could not connect to "+cn.Config.Name, err.Error())
				return
			}
			cn.Reset()
			cn.Status, cn.DB, cn.KV, cn.Version = connection.StatusConnected, sqldb, kv, version
			waiters := cn.Waiters
			cn.Waiters = nil
			if cn.DB != nil {
				a.loadSchemas(cn, "")
			}
			for _, w := range waiters {
				w()
			}
		}
	})
}

// askHostKey asks whether to trust an SSH server seen for the first time.
func (a *App) askHostKey(cn *connection.Conn, cfg db.Config, hk *sshtunnel.HostKeyError) {
	if hk.Mismatch {
		a.ShowError("The SSH host key changed",
			fmt.Sprintf("%s presented the key %s, which differs from the one in known_hosts. "+
				"Someone may be intercepting the connection, or the server was reinstalled. "+
				"Check with its administrator and remove the old key from known_hosts before connecting.", hk.Host, hk.Fingerprint))
		return
	}
	a.hostKey = &hostKeyRequest{open: true, host: hk.Host, fingerprint: hk.Fingerprint, onTrust: func() {
		if err := sshtunnel.Trust(a.appKnownHosts, hk.Host, hk.Key); err != nil {
			a.ShowError("Could not save the host key", err.Error())
			return
		}
		a.Record(&cfg, audit.Event{Kind: audit.KindTrust, Detail: "trusted SSH host " + hk.Host + " with key " + hk.Fingerprint})
		a.open(cn, cfg)
	}}
}

func (a *App) disconnect(cn *connection.Conn) {
	for i := len(a.tabs) - 1; i >= 0; i-- {
		if a.tabs[i].Connection() == cn {
			a.closeTabNow(i)
		}
	}
	cn.Generation++
	sqldb, kv := cn.DB, cn.KV
	if sqldb != nil || kv != nil {
		a.Record(&cn.Config, audit.Event{Kind: audit.KindDisconnect})
	}
	cn.DB, cn.KV, cn.Status, cn.Err = nil, nil, connection.StatusIdle, ""
	cn.Reset()
	go func() {
		if sqldb != nil {
			sqldb.Close()
		}
		if kv != nil {
			kv.Close()
		}
	}()
}

// loadSchemas reads the schemas of a connection's database, and opens its
// default schema in the navigator when it was asked to.
func (a *App) loadSchemas(cn *connection.Conn, database string) {
	connection.LoadSchemas(a, cn, database, func() {
		if database == "" && cn.ExpandWhenLoaded {
			cn.ExpandWhenLoaded = false
			a.expandDefaults(cn)
		}
	})
}

// refresh forgets what is known of a connection's schema.
func (a *App) refresh(cn *connection.Conn) {
	if cn.Status != connection.StatusConnected {
		return
	}
	cn.Reset()
	if cn.DB != nil {
		a.loadSchemas(cn, "")
	}
}

// Tabs.

func (a *App) AddTab(t widgets.Tab) {
	a.tabs = append(a.tabs, t)
	a.active = len(a.tabs) - 1
}

// ReplaceTab puts a tab in the place of another, which closes.
func (a *App) ReplaceTab(old, next widgets.Tab) {
	if i := slices.Index(a.tabs, old); i >= 0 {
		a.tabs[i] = next
		old.Close()
	}
}

func (a *App) ActiveTab() widgets.Tab {
	if a.active >= 0 && a.active < len(a.tabs) {
		return a.tabs[a.active]
	}
	return nil
}

func (a *App) closeTab(i int) {
	if i < 0 || i >= len(a.tabs) {
		return
	}
	t := a.tabs[i]
	a.endTab(t, "Close "+t.Title()+"?", func(int) {})
}

// endTab closes a tab once the user agrees to what closing it loses, at
// once when it loses nothing, then runs then with the place it had.
func (a *App) endTab(t widgets.Tab, title string, then func(at int)) {
	end := func() {
		if i := slices.Index(a.tabs, t); i >= 0 {
			a.closeTabNow(i)
			then(i)
		}
	}
	reason := t.CloseReason()
	if reason == "" {
		end()
		return
	}
	a.closing = &closeRequest{open: true, title: title, reason: reason, onClose: end}
	if h, ok := t.(txHolder); ok && h.OpenTx() {
		a.closing.txs = []txHolder{h}
	}
}

func (a *App) closeTabNow(i int) {
	t := a.tabs[i]
	a.tabs = slices.Delete(a.tabs, i, i+1)
	if a.active >= len(a.tabs) || a.active > i {
		a.active--
	}
	a.active = max(0, min(a.active, len(a.tabs)-1))
	t.Close()
}

// NewQueryTab opens a SQL editor on a new query file of a connection.
func (a *App) NewQueryTab(cn *connection.Conn, database, text string) {
	if cn.Config.Engine == db.Redis {
		a.openRedis(cn)
		return
	}
	a.newQueryFile(cn, database, text, nil)
}

// OpenTable opens a table's data, or brings its tab forward.
func (a *App) OpenTable(cn *connection.Conn, database string, obj db.Object, page int) {
	for i, t := range a.tabs {
		if tt, ok := t.(*dataview.TableTab); ok && tt.Conn == cn && tt.Database == database && tt.Object.Schema == obj.Schema && tt.Object.Name == obj.Name {
			a.active = i
			tt.Page = page
			return
		}
	}
	a.Connect(cn, func() {
		a.AddTab(dataview.NewTableTab(a, cn, database, obj, page))
	})
}

func (a *App) openRedis(cn *connection.Conn) {
	for i, t := range a.tabs {
		if rt, ok := t.(*redis.Tab); ok && rt.Connection() == cn {
			a.active = i
			return
		}
	}
	a.Connect(cn, func() {
		a.AddTab(redis.New(a, cn))
	})
}

// Dialogs.

type alertRequest struct {
	open           bool
	title, message string
}

func (a *App) ShowError(title, message string) {
	a.alert = &alertRequest{open: true, title: title, message: message}
}

// pendingAsk asks what becomes of changes not applied yet before their
// rows are read again.
type pendingAsk struct {
	open                   bool
	n                      int
	apply, discard, cancel func()
}

type closeRequest struct {
	open          bool
	title, reason string
	onClose       func()
	// txs are the open transactions closing would end: the dialog offers
	// to commit them first, else onClose rolls them back.
	txs []txHolder
}

// losses lists what closing the tabs of some connections would lose:
// unsaved changes, pending edits and open transactions.
func (a *App) losses(conns ...*connection.Conn) []string {
	var out []string
	for _, t := range a.tabs {
		for _, cn := range conns {
			if t.Connection() == cn {
				if r := t.CloseReason(); r != "" {
					out = append(out, t.Title()+": "+r)
				}
			}
		}
	}
	return out
}

// requestDisconnect runs then, which disconnects, once the user agrees to
// lose what the tabs of the connections hold; at once when they hold
// nothing.
func (a *App) requestDisconnect(title string, conns []*connection.Conn, then func()) {
	lost := a.losses(conns...)
	if len(lost) == 0 {
		then()
		return
	}
	a.closing = &closeRequest{open: true, title: title, reason: strings.Join(lost, "\n\n"), onClose: then, txs: a.openTxTabs(conns...)}
}

// requestQuit reports whether the app may quit now. When tabs would lose
// work, as an open transaction, it asks first, and quit runs once the
// user agrees.
func (a *App) requestQuit(quit func()) bool {
	if a.quitting || len(a.losses(a.conns...)) == 0 {
		return true
	}
	if a.closing == nil {
		a.requestDisconnect("Quit DGopher?", a.conns, func() {
			a.quitting = true
			quit()
		})
	}
	return false
}

// sharedFingerprint identifies a connection by where it sends a
// password, how safely, and how it gets one: a change to any, as in a
// pull of the project, asks again.
func sharedFingerprint(cfg *db.Config, projectDir string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%s|%d|%s|%s|%s|%q|%v|%s|%d|%s|%q|%s|%q|%q%s", projectDir, cfg.ID, cfg.Engine, cfg.Host, cfg.Port,
		cfg.User, cfg.Database, cfg.TLS, cfg.CAFile, cfg.SSH.Enabled, cfg.SSH.Host, cfg.SSH.Port, cfg.SSH.User, cfg.SSH.KeyPath,
		cfg.PasswordEnv, cfg.PasswordCommand, cfg.SSH.PasswordCommand, redisWhere(cfg)+clientCertWhere(cfg)+jumpWhere(cfg)+proxyWhere(cfg))))
	return hex.EncodeToString(sum[:])
}

func (a *App) trusted(cn *connection.Conn) bool {
	return slices.Contains(a.settings.TrustedShared, sharedFingerprint(&cn.Config, cn.Project.Dir))
}

// trust records that the user agreed to the connection as it is.
func (a *App) trust(cn *connection.Conn) {
	if a.trusted(cn) {
		return
	}
	a.settings.TrustedShared = append(a.settings.TrustedShared, sharedFingerprint(&cn.Config, cn.Project.Dir))
	a.SaveSettings()
}

// sharedDestination says where a shared connection would send what.
func sharedDestination(cfg *db.Config, projectName string) string {
	where := cfg.Engine.Label() + " at " + cfg.User + "@" + cfg.Host
	if cfg.Port > 0 {
		where += fmt.Sprintf(":%d", cfg.Port)
	}
	if cfg.Engine.IsFile() {
		where = cfg.Engine.Label() + " file " + cfg.Database
	}
	switch cfg.Redis.Mode {
	case db.RedisCluster:
		where += " and the nodes of its cluster"
		if cfg.Redis.Nodes != "" {
			where += ", " + cfg.Redis.Nodes + " among them"
		}
	case db.RedisSentinel:
		where = "the Redis master " + cfg.Redis.Master + " that the sentinels at " + cfg.Host + ":" + strconv.Itoa(cfg.Port)
		if cfg.Redis.Nodes != "" {
			where += ", " + cfg.Redis.Nodes
		}
		where += " name"
	}
	if cfg.SSH.Enabled {
		where += ", through SSH " + cfg.SSH.User + "@" + cfg.SSH.Host
	}
	if cfg.Engine.IsFile() {
		return "The project " + projectName + " (" + project.File + ") asks to open the " + where + ". Open it only if you expect this file."
	}
	secret := "the password you type or keep for it"
	switch {
	case cfg.PasswordCommand != "":
		secret = "the password printed by running the command " + cfg.PasswordCommand + " on this computer"
	case cfg.PasswordEnv != "":
		secret = "the password in $" + cfg.PasswordEnv
	}
	if cfg.SSH.Enabled && cfg.SSH.PasswordCommand != "" {
		secret += ", and the SSH secret printed by running the command " + cfg.SSH.PasswordCommand
	}
	return "The project " + projectName + " (" + project.File + ") asks to connect to " + where + ", sending " + secret + ". Connect only if you expect this destination and these commands."
}
