// Package app is the DGopher window: the navigator, the tabs, and the
// dialogs that act across them.
package app

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
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
	"dgopher/internal/keymap"
	"dgopher/internal/project"
	"dgopher/internal/safety"
	"dgopher/internal/secretcmd"
	"dgopher/internal/settings"
	"dgopher/internal/sshtunnel"
	"dgopher/internal/state"
	"dgopher/internal/store"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/redis"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// App is the state of the app and its windows.
type App struct {
	// window is the window tabs open in and come forward in: the one
	// being drawn, and while posted work runs, the one the user last used.
	*window
	// drawing is the window a frame is drawing, nil between frames: a tab
	// brought forward then stays in its window, which comes to the front.
	drawing     *window
	main        *window
	windows     []*window
	lastFocused *window
	// makeWindow makes the system's window of a window, nil without the
	// system's windows, as in tests.
	makeWindow func(*window)
	// windowFocused reports whether a window of the app has the keyboard
	// focus; notify shows a system notification, whose click runs onClick
	// off the main thread. Both are the system's, nil without its windows.
	windowFocused func() bool
	notify        func(title, body string, onClick func())
	st            *store.Store

	settings settings.Settings
	conns    []*connection.Conn

	// Work finished on other goroutines, applied as the next frame starts.
	queueMu sync.Mutex
	queue   []func()

	// jobs are the work running on connections (StartJob), which ends on
	// other goroutines.
	jobsMu sync.Mutex
	jobs   []*job

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
	// catalogQueries shows the catalog queries of a connection.
	catalogQueries *catalogView
	backup         *backupDialog
	copying        *copyDialog
	rowCompare     *rowCompareDialog
	filling        *fillDialog
	addingPanel    *addToDashboard
	savingModel    *saveModelForm
	keys           *keysEditor
	themes         []widgets.Theme // the built-in and the user's
	// The guided tour: whether it shows, its stop, and the bounds of the
	// parts of the window it shows, as the last frame laid them out.
	touring   bool
	tourStop  int
	tourParts map[string]ui.Rect
	newModel  *newModelForm
	// fileLists are the projects' dashboards and models, by folder: a
	// menu asks for them each frame it shows.
	fileLists   map[string]namedFiles
	quitting    bool // the user agreed to what quitting loses
	idleWarn    *idleWarning
	auditView   *auditState
	snippetForm *snippetForm
	history     *historyState
	toast       *pendingToast

	settingsOpen  bool
	settingsPage  int // an index of settingsPages
	shortcutsOpen bool
	// shortcutsFind filters the list of keys.
	shortcutsFind string
	settingsDirty bool
	// layout is the windows' layout, which ui keeps, when there is one,
	// as layoutSaved was last written to it.
	layout, layoutSaved widgets.Layout
	ui                  *state.DB
	clipboard           func(string)
	readClip            func() string

	// auditLogs are the projects' audit logs by the prefix of their
	// connections' IDs, for record, which may run on any goroutine.
	auditMu   sync.Mutex
	auditLogs map[string]*audit.Log

	appKnownHosts  string
	now            time.Time
	workspaceSaved time.Time
}

func newApp(st *store.Store) *App {
	a := &App{st: st, settings: settings.Default(), layout: widgets.DefaultLayout()}
	a.layoutSaved = a.layout
	a.main = &window{}
	a.window, a.windows = a.main, []*window{a.main}
	if err := st.LoadJSON("settings.json", &a.settings); err != nil && !errors.Is(err, store.ErrNotFound) {
		log.Println("settings:", err)
	}
	for _, err := range keymap.Use(a.settings.Keys) {
		log.Println("settings: keys:", err)
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
	a.invalidate()
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
// runs on the main thread. A panic of the work is shown, and the app goes
// on.
func (a *App) Background(work func() func()) {
	go func() {
		defer dataview.RecoverBackground(a.Post, a.ShowError, nil)
		apply := work()
		if apply != nil {
			a.Post(apply)
		}
	}()
}

// job is work running on a connection, as Run SQL File or an import:
// quitting, disconnecting and deleting the connection list it with what
// stopping it loses, then stop it. done closes once it ended.
type job struct {
	conn   *connection.Conn
	title  string
	cancel context.CancelFunc
	done   chan struct{}
}

// jobStopWait is how long disconnecting and quitting wait for the jobs
// they stop to end, rolling back what they left open, before the
// connections close. A variable so tests can shorten it.
var jobStopWait = 10 * time.Second

// StartJob registers a job, as dataview.Host says.
func (a *App) StartJob(cn *connection.Conn, title string, cancel context.CancelFunc) (finish func()) {
	j := &job{conn: cn, title: title, cancel: cancel, done: make(chan struct{})}
	a.jobsMu.Lock()
	a.jobs = append(a.jobs, j)
	a.jobsMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			a.jobsMu.Lock()
			a.jobs = slices.DeleteFunc(a.jobs, func(x *job) bool { return x == j })
			a.jobsMu.Unlock()
			close(j.done)
		})
	}
}

// runningJobs lists the jobs running, on every connection.
func (a *App) runningJobs() []*job {
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	return slices.Clone(a.jobs)
}

// jobsOf lists the jobs running on the connections given.
func (a *App) jobsOf(conns ...*connection.Conn) []*job {
	var out []*job
	for _, j := range a.runningJobs() {
		if slices.Contains(conns, j.conn) {
			out = append(out, j)
		}
	}
	return out
}

// stopJobs cancels jobs, then waits for them to end, up to jobStopWait in
// all.
func stopJobs(jobs []*job) {
	for _, j := range jobs {
		j.cancel()
	}
	timeout := time.After(jobStopWait)
	for _, j := range jobs {
		select {
		case <-j.done:
		case <-timeout:
			return
		}
	}
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
// as when a shared project file changes the host of a connection, nor
// through a proxy, with a client certificate, through jump hosts or with
// a cloud identity it was not typed for.
func secretKey(cfg *db.Config, what string) string {
	// A cluster's nodes, the sentinels, the client certificate, the jump
	// hosts, the proxy and the identity are where a password goes too.
	// They join only when set, for the keys of the passwords kept before
	// they existed to stay as they were.
	return keyOf(cfg, what, serverWhere(cfg)+clientCertWhere(cfg)+jumpWhere(cfg)+proxyWhere(cfg)+identityWhere(cfg))
}

// legacySecretKey is the key a secret was kept under before the client
// certificate, the jump hosts, the proxy and the identity joined it: the
// same as secretKey for a connection without them.
func legacySecretKey(cfg *db.Config, what string) string {
	return keyOf(cfg, what, serverWhere(cfg))
}

// keyOf is the keychain key of a secret of a connection that goes where
// says; an SSH secret goes to the SSH host alone.
func keyOf(cfg *db.Config, what, where string) string {
	if strings.HasPrefix(what, "ssh-") {
		where = fmt.Sprintf("ssh|%s|%d|%s|%s", cfg.SSH.Host, cfg.SSH.Port, cfg.SSH.User, cfg.SSH.KeyPath)
	}
	sum := sha256.Sum256([]byte(where))
	return "conn/" + cfg.ID + "/" + hex.EncodeToString(sum[:6]) + "/" + what
}

// serverWhere is the server a connection's password goes to, and its SSH
// tunnel.
func serverWhere(cfg *db.Config) string {
	return fmt.Sprintf("%s|%s|%d|%s|%s|%v|%s|%d|%s%s", cfg.Engine, cfg.Host, cfg.Port, cfg.User, cfg.Database,
		cfg.SSH.Enabled, cfg.SSH.Host, cfg.SSH.Port, cfg.SSH.User, redisWhere(cfg))
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

// identityWhere is the cloud identity a connection logs in with, and
// whether it sends its password as clear text, "" for neither, which
// keeps the fingerprints of connections without them.
func identityWhere(cfg *db.Config) string {
	if cfg.Identity == "" && !cfg.ClearTextPassword {
		return ""
	}
	return fmt.Sprintf("|identity|%s|%s|%s|%v", cfg.Identity, cfg.IdentityRegion, cfg.IdentityProfile, cfg.ClearTextPassword)
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
		if old := legacySecretKey(cfg, what); old != secretKey(cfg, what) {
			sec.Delete(old)
		}
	}
}

// passwordLost reports whether a connection whose password is kept in the
// keychain had one under the key of before (legacySecretKey) and has none
// under its own: as after the proxy, the certificate, the jump hosts or the
// identity joined its key, or a pull added one of them. The old password is
// not for where it goes now, so it is asked for again.
func (a *App) passwordLost(cfg *db.Config) bool {
	if cfg.Password != "" || sourceOf(cfg) != sourceKeychain || cfg.Engine.IsFile() {
		return false
	}
	old := legacySecretKey(cfg, "password")
	if old == secretKey(cfg, "password") {
		return false
	}
	sec := a.st.Secrets()
	if !sec.Available() {
		return false
	}
	_, err := sec.Get(old)
	return err == nil
}

// keepPassword keeps a password typed again for a connection whose
// keychain lost it, and forgets the one kept under the key of before.
func (a *App) keepPassword(cfg *db.Config, pw string) {
	sec := a.st.Secrets()
	if err := sec.Set(secretKey(cfg, "password"), pw); err != nil {
		a.ShowError("Could not save the password in the keychain", err.Error())
		return
	}
	sec.Delete(legacySecretKey(cfg, "password"))
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
	if ask, details := a.trustCheck(cn); ask {
		// A file from a repository chose this destination: the user says
		// whether a password may go there, once per destination.
		cn.Waiters = nil
		if a.asking(func(r *widgets.ConfirmRequest) bool { return r.Trust && r.Conn == cn }) {
			return // asking already
		}
		r := a.askTrust(cn, &cfg, details, "Connect to "+cfg.Name+"?", "Connect", func() {
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
	// What a later pull is compared with: the connection as it was when
	// trusted before the details were kept, or with its care raised since.
	a.trust(cn)
	if why := envAllowed(&cfg); why != "" {
		cn.Status, cn.Err = connection.StatusFailed, why
		cn.Waiters = nil
		a.ShowError("Could not connect to "+cfg.Name, why)
		return
	}
	cn.Status, cn.Err = connection.StatusConnecting, ""
	gen := cn.Generation
	dataview.BackgroundResetOnPanic(a, func() {
		if gen == cn.Generation {
			cn.Status, cn.Err = connection.StatusFailed, "stopped on an internal error"
			cn.Waiters = nil
		}
	}, func() func() {
		a.loadSecrets(&cfg) // the keychain can take a while to answer
		lost := a.passwordLost(&cfg)
		return func() {
			if gen != cn.Generation {
				return // disconnected meanwhile
			}
			a.askPasswordOrOpen(cn, cfg, lost)
		}
	})
}

// askPasswordOrOpen opens a connection, once asked for its password when
// it is asked every time, or when the keychain lost it (passwordLost),
// which the user may then keep.
func (a *App) askPasswordOrOpen(cn *connection.Conn, cfg db.Config, lost bool) {
	if (cfg.AskPassword || lost) && cfg.Password == "" && !cfg.Engine.IsFile() {
		if a.prompt != nil && a.prompt.conn == cn || slices.ContainsFunc(a.promptQueue, func(p *passwordPrompt) bool { return p.conn == cn }) {
			return // asking already
		}
		p := &passwordPrompt{conn: cn, open: true, name: cfg.Name, where: cfg.User + "@" + cfg.Host, action: "Connect", offerKeep: lost, keep: lost}
		p.onSubmit = func(pw string) {
			if p.keep && pw != "" {
				a.keepPassword(&cfg, pw)
			}
			cfg.Password = pw
			a.open(cn, cfg)
		}
		p.onCancel = func() {
			cn.Waiters = nil
			// Not idle: what connects as it shows would ask again at once.
			cn.Status, cn.Err = connection.StatusFailed, "no password was given"
		}
		a.askPassword(p)
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
	// What the work opened, which the reset closes, tunnel and all, when
	// the work stops on a panic before the main thread takes it.
	var sqldb *db.DB
	var kv *db.KV
	dataview.BackgroundResetOnPanic(a, func() {
		if sqldb != nil {
			go sqldb.Close()
		}
		if kv != nil {
			go kv.Close()
		}
		if gen == cn.Generation {
			cn.Status, cn.Err = connection.StatusFailed, "stopped on an internal error"
			cn.Waiters = nil
		}
	}, func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second+secretcmd.Timeout)
		defer cancel()
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
				// Without what a password command printed: it may hold a
				// secret, which the live error below shows.
				ev.Err = err
			} else {
				if version != "" {
					ev.Detail += ", server " + widgets.FirstLine(version)
				}
				if s := tlsStateOf(sqldb, kv); s != "" {
					ev.Detail += ", " + s
				}
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
	for _, t := range a.everyTab() {
		if t.Connection() == cn {
			a.removeTab(t)
			t.Close()
		}
	}
	jobs := a.jobsOf(cn)
	cn.Generation++
	sqldb, kv := cn.DB, cn.KV
	if sqldb != nil || kv != nil {
		a.Record(&cn.Config, audit.Event{Kind: audit.KindDisconnect})
	}
	cn.DB, cn.KV, cn.Status, cn.Err = nil, nil, connection.StatusIdle, ""
	cn.Reset()
	go func() {
		defer dataview.RecoverBackground(a.Post, a.ShowError, nil)
		// The work running on the pool ends first, rolling back what it
		// left open there.
		stopJobs(jobs)
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
	for w, t := range a.everyTab() {
		if t == old {
			w.tabs[slices.Index(w.tabs, old)] = next
			old.Close()
			return
		}
	}
}

// KeysTo reports whether a tab takes the shortcuts pressed: the active
// one, which is the focused pane's when two tabs show side by side.
func (a *App) KeysTo(t widgets.Tab) bool { return t != nil && t == a.ActiveTab() }

func (a *App) ActiveTab() widgets.Tab { return a.window.ActiveTab() }

func (a *App) closeTab(i int) {
	if i < 0 || i >= len(a.tabs) {
		return
	}
	a.closeTabOf(a.tabs[i])
}

// closeTabOf closes a tab of any window, once the user agrees to what
// closing it loses.
func (a *App) closeTabOf(t widgets.Tab) {
	a.endTab(t, "Close "+t.Title()+"?", func(*window, int) {})
}

// endTab closes a tab once the user agrees to what closing it loses, at
// once when it loses nothing, then runs then with the window and the
// place it had.
func (a *App) endTab(t widgets.Tab, title string, then func(w *window, at int)) {
	end := func() {
		if w, i, ok := a.removeTab(t); ok {
			t.Close()
			then(w, i)
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
	if t, ok := a.findTab(func(t widgets.Tab) bool {
		tt, ok := t.(*dataview.TableTab)
		return ok && tt.Conn == cn && tt.Database == database && tt.Object.Schema == obj.Schema && tt.Object.Name == obj.Name
	}); ok {
		t.(*dataview.TableTab).Page = page
		return
	}
	a.Connect(cn, func() {
		a.AddTab(dataview.NewTableTab(a, cn, database, obj, page))
	})
}

func (a *App) openRedis(cn *connection.Conn) {
	if a.ActivateTab(func(t widgets.Tab) bool { rt, ok := t.(*redis.Tab); return ok && rt.Connection() == cn }) {
		return
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
	// action is the button that agrees, "Close" when empty.
	action  string
	onClose func()
	// txs are the open transactions closing would end: the dialog offers
	// to commit them first, else onClose rolls them back.
	txs []txHolder
}

// losses lists what closing the tabs of some connections would lose:
// unsaved changes, pending edits and open transactions; and the work
// running on them, with what stopping it loses.
func (a *App) losses(conns ...*connection.Conn) []string {
	var out []string
	for _, t := range a.everyTab() {
		for _, cn := range conns {
			if t.Connection() == cn {
				if r := t.CloseReason(); r != "" {
					out = append(out, t.Title()+": "+r)
				}
			}
		}
	}
	for _, j := range a.jobsOf(conns...) {
		// A job of two connections, as a copy, is listed once.
		if !slices.Contains(out, j.title) {
			out = append(out, j.title)
		}
	}
	return out
}

// requestDisconnect runs then, which disconnects, once the user agrees to
// lose what the tabs of the connections hold and to stop the work running
// on them; at once when there is none.
func (a *App) requestDisconnect(title string, conns []*connection.Conn, then func()) {
	lost := a.losses(conns...)
	if len(lost) == 0 {
		then()
		return
	}
	a.closing = &closeRequest{open: true, title: title, reason: strings.Join(lost, "\n\n"), onClose: then, txs: a.openTxTabs(conns...)}
}

// requestQuit reports whether the app may quit now. When tabs would lose
// work, as an open transaction, or work runs on a connection, as Run SQL
// File, it asks first, and quit runs once the user agrees.
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
		cfg.PasswordEnv, cfg.PasswordCommand, cfg.SSH.PasswordCommand, redisWhere(cfg)+clientCertWhere(cfg)+jumpWhere(cfg)+proxyWhere(cfg)+identityWhere(cfg))))
	return hex.EncodeToString(sum[:])
}

// trusted reports whether a connection may connect without asking: the
// user agreed to it as it is (trustCheck).
func (a *App) trusted(cn *connection.Conn) bool {
	ask, _ := a.trustCheck(cn)
	return !ask
}

// trust records that the user agreed to the connection as it is: its
// fingerprint, and the details a later prompt compares with.
func (a *App) trust(cn *connection.Conn) {
	changed := false
	if fp := sharedFingerprint(&cn.Config, cn.Project.Dir); !slices.Contains(a.settings.TrustedShared, fp) {
		a.settings.TrustedShared = append(a.settings.TrustedShared, fp)
		changed = true
	}
	now := trustSnapshot(&cn.Config)
	key := trustKey(cn)
	if was, ok := a.settings.TrustedDetails[key]; !ok || !slices.Equal(was.Rows, now.Rows) || was.Env != now.Env ||
		was.ReadOnly != now.ReadOnly || was.ManualCommit != now.ManualCommit {
		if a.settings.TrustedDetails == nil {
			a.settings.TrustedDetails = map[string]settings.TrustedDetail{}
		}
		a.settings.TrustedDetails[key] = now
		changed = true
	}
	if changed {
		a.SaveSettings()
	}
}

// trustKey is what the details a user agreed to are kept by: the project
// folder and the connection's ID.
func trustKey(cn *connection.Conn) string { return cn.Project.Dir + "\x00" + cn.Config.ID }

// trustSnapshot is a connection as the user agrees to it, without a
// secret.
func trustSnapshot(cfg *db.Config) settings.TrustedDetail {
	return settings.TrustedDetail{Rows: detailRows(secretDetails(cfg)), Env: cfg.Env, ReadOnly: cfg.ReadOnly, ManualCommit: cfg.ManualCommit()}
}

// detailRows are details as a snapshot keeps them, "Label: value".
func detailRows(details []widgets.ConfirmDetail) []string {
	rows := make([]string, len(details))
	for i, d := range details {
		rows[i] = d.Label + ": " + d.Value
	}
	return rows
}

// trustCheck compares a connection with what the user last agreed to:
// whether to ask before it connects, and the details to show, those that
// changed since marked. It asks when its fingerprint is new, when where
// its secrets go or how safely changed, or when the care taken with it
// dropped. A connection trusted before the details were kept has none to
// compare with: it asks only for a new fingerprint, and trust keeps the
// values it has as the baseline.
func (a *App) trustCheck(cn *connection.Conn) (ask bool, details []widgets.ConfirmDetail) {
	cfg := &cn.Config
	ask = !slices.Contains(a.settings.TrustedShared, sharedFingerprint(cfg, cn.Project.Dir))
	details = secretDetails(cfg)
	was, ok := a.settings.TrustedDetails[trustKey(cn)]
	if !ok {
		return ask, append(details, protectionDetails(cfg, nil)...)
	}
	now := detailRows(details)
	for i, row := range now {
		if !slices.Contains(was.Rows, row) {
			details[i].Changed, ask = true, true
		}
	}
	if slices.ContainsFunc(was.Rows, func(row string) bool { return !slices.Contains(now, row) }) {
		ask = true
	}
	return ask || protectionDropped(was, cfg), append(details, protectionDetails(cfg, &was)...)
}

// protectionDropped reports whether a connection is treated with less
// care than when the user agreed to it: a less careful environment,
// read-only no more, or auto-commit where commits were manual. More care
// asks nothing.
func protectionDropped(was settings.TrustedDetail, cfg *db.Config) bool {
	rank := func(e db.Environment) int { return slices.Index(db.Environments(), db.NormalizeEnvironment(e)) }
	return rank(cfg.Env) < rank(was.Env) || was.ReadOnly && !cfg.ReadOnly || was.ManualCommit && !cfg.ManualCommit()
}

// askTrust asks whether a shared connection may connect as cfg says,
// listing the details, with what changed since the user last agreed.
func (a *App) askTrust(cn *connection.Conn, cfg *db.Config, details []widgets.ConfirmDetail, title, action string, onConfirm func()) *widgets.ConfirmRequest {
	reasons := []string{sharedDestination(cfg, cn.Project.Name)}
	if slices.ContainsFunc(details, func(d widgets.ConfirmDetail) bool { return d.Changed }) {
		reasons = append(reasons, "What changed since you last agreed to it is marked.")
	}
	r := a.AskConfirm(cn, safety.Verdict{Reasons: reasons}, title, action, "", onConfirm)
	r.Details = details
	return r
}

// secretDetails lists what decides where a connection sends its secrets,
// how safely, and what it runs to get them, a row each: what the user
// agrees to before a shared connection connects. None holds a secret.
func secretDetails(cfg *db.Config) []widgets.ConfirmDetail {
	var out []widgets.ConfirmDetail
	add := func(label, value string) { out = append(out, widgets.ConfirmDetail{Label: label, Value: value}) }
	file := cfg.Engine.IsFile()
	if file {
		add("File", serverText(cfg))
	} else {
		add("Server", serverText(cfg))
		add("TLS", tlsText(cfg.TLS))
		add("CA file", cmp.Or(cfg.CAFile, "none: the system's"))
		cert := "none"
		if cfg.CertFile != "" {
			cert = cfg.CertFile + ", key " + cfg.KeyFile
		}
		add("Client certificate", cert)
		proxy := "none"
		if p := cfg.Proxy; p.Kind != "" {
			proxy = strings.ToUpper(p.Kind) + " " + net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
			if p.User != "" {
				proxy = strings.ToUpper(p.Kind) + " " + p.User + "@" + net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
			}
		}
		add("Proxy", proxy)
		ssh := "none"
		if s := cfg.SSH; s.Enabled {
			ssh = s.User + "@" + net.JoinHostPort(s.Host, strconv.Itoa(cmp.Or(s.Port, 22)))
			if s.KeyPath != "" {
				ssh += ", key " + s.KeyPath
			}
			if s.UseAgent {
				ssh += ", the SSH agent"
			}
		}
		add("SSH", ssh)
		if cfg.SSH.Enabled {
			add("Jump hosts", cmp.Or(cfg.SSH.Jump, "none"))
		}
	}
	switch src := sourceOf(cfg); {
	case src == sourceCommand:
		add("Password", "printed by running "+cfg.PasswordCommand+" on this computer")
	case src == sourceEnv:
		add("Password", "the variable $"+cfg.PasswordEnv+" of this computer")
	case file:
	case src == sourceIdentity:
		add("Password", "none: the identity's token takes its place")
	case src == sourceAsk:
		add("Password", "asked for at each connect")
	default:
		add("Password", "the one kept for it in this computer's keychain, if any")
	}
	// A file may name a command or a variable beside the password's
	// source: they run, or are read, when the source gives nothing.
	if cfg.PasswordCommand != "" && sourceOf(cfg) != sourceCommand {
		add("Password command", "runs "+cfg.PasswordCommand+" on this computer")
	}
	if cfg.PasswordEnv != "" && sourceOf(cfg) != sourceEnv {
		add("Password variable", "$"+cfg.PasswordEnv)
	}
	switch {
	case cfg.SSH.Enabled && cfg.SSH.PasswordCommand != "":
		add("SSH secret", "printed by running "+cfg.SSH.PasswordCommand+" on this computer")
	case cfg.SSH.Enabled && !file:
		add("SSH secret", "the one kept for it in this computer's keychain, if any")
	}
	if cfg.Identity != "" {
		add("Identity", cfg.Identity.Label()+": runs "+commandText(db.IdentityCommand(cfg))+" on this computer, and sends the cloud token it prints")
	}
	if cfg.Engine == db.MySQL || cfg.ClearTextPassword {
		clear := "no"
		if cfg.ClearTextPassword {
			clear = "yes: the password goes as it is, as LDAP and PAM logins need"
		}
		add("Clear-text password", clear)
	}
	return out
}

// protectionDetails says how carefully the app treats a connection, each
// row marked when it differs from was, when the user agreed to one.
func protectionDetails(cfg *db.Config, was *settings.TrustedDetail) []widgets.ConfirmDetail {
	readOnly := "no"
	if cfg.ReadOnly {
		readOnly = "yes: the app refuses writes"
	}
	out := []widgets.ConfirmDetail{
		{Label: "Environment", Value: cfg.Env.Label(), Changed: was != nil && db.NormalizeEnvironment(was.Env) != db.NormalizeEnvironment(cfg.Env)},
		{Label: "Read-only", Value: readOnly, Changed: was != nil && was.ReadOnly != cfg.ReadOnly},
	}
	if cfg.Engine.Transactions() {
		commit := "automatic: each statement commits"
		if cfg.ManualCommit() {
			commit = "manual: writes wait for Commit"
		}
		out = append(out, widgets.ConfirmDetail{Label: "Commit", Value: commit, Changed: was != nil && was.ManualCommit != cfg.ManualCommit()})
	}
	return out
}

// tlsText says how safely a TLS mode sends the password.
func tlsText(m db.TLSMode) string {
	switch m {
	case db.TLSPrefer:
		return "prefer, unverified: TLS if the server offers it, its certificate not checked; else none"
	case db.TLSRequire:
		return "require, unverified: TLS, the server's certificate not checked"
	case db.TLSVerifyFull:
		return "verify-full: TLS, the server's certificate and host name checked"
	}
	return "off: no TLS, the password and the data go as they are"
}

// commandText writes a command's words as a shell would read them back,
// quoting a word with a space, a quote or another such character.
func commandText(argv []string) string {
	words := make([]string, len(argv))
	for i, w := range argv {
		plain := w != ""
		for _, r := range w {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:=@,+%", r)) {
				plain = false
			}
		}
		if plain {
			words[i] = w
		} else {
			words[i] = "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
		}
	}
	return strings.Join(words, " ")
}

// serverText says which server a connection goes to: the engine, the user
// and the address, and the database; a file's path.
func serverText(cfg *db.Config) string {
	if cfg.Engine.IsFile() {
		return cfg.Engine.Label() + " file " + cfg.Database
	}
	where := cfg.Engine.Label() + " at " + cfg.User + "@" + cfg.Host
	if cfg.Port > 0 {
		where += fmt.Sprintf(":%d", cfg.Port)
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
		if cfg.Redis.SentinelUser != "" {
			where += ", logging in to them as " + cfg.Redis.SentinelUser
		}
	}
	if cfg.Database != "" {
		where += ", database " + cfg.Database
	}
	return where
}

// sharedDestination says where a shared connection would send what.
func sharedDestination(cfg *db.Config, projectName string) string {
	where := serverText(cfg)
	if cfg.SSH.Enabled {
		where += ", through SSH " + cfg.SSH.User + "@" + cfg.SSH.Host
	}
	if cfg.Engine.IsFile() {
		return "The project " + projectName + " (" + project.File + ") asks to open the " + where + ". Open it only if you expect this file."
	}
	if cfg.Proxy.Kind != "" {
		where += ", through the proxy " + cfg.Proxy.Host
	}
	secret := "the password you type or keep for it"
	switch {
	case cfg.Identity != "":
		secret = "a token of your cloud login, printed by running the command " + commandText(db.IdentityCommand(cfg)) + " on this computer"
	case cfg.PasswordCommand != "":
		secret = "the password printed by running the command " + cfg.PasswordCommand + " on this computer"
	case cfg.PasswordEnv != "":
		secret = "the password in $" + cfg.PasswordEnv
	}
	if cfg.ClearTextPassword && cfg.Identity == "" {
		secret += " as clear text"
	}
	if cfg.SSH.Enabled && cfg.SSH.PasswordCommand != "" {
		secret += ", and the SSH secret printed by running the command " + cfg.SSH.PasswordCommand
	}
	return "The project " + projectName + " (" + project.File + ") asks to connect to " + where + ", sending " + secret + ". Connect only if you expect this destination and these commands."
}
