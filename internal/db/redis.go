package db

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"dgopher/internal/sshtunnel"

	"github.com/redis/rueidis"
)

// KV is an open Redis connection.
type KV struct {
	Config Config
	Client rueidis.Client
	tunnel *sshtunnel.Tunnel
	// readOnly holds the names of the server's read-only commands, from
	// COMMAND; nil when the server does not list them.
	readOnly map[string]bool

	// masters are the clients of a cluster's masters, by address, as the
	// last scan from the start found them.
	mu      sync.Mutex
	masters []rueidis.Client

	// docs are the commands' help, read once by CommandDocs.
	docsMu sync.Mutex
	docs   map[string]CommandDoc

	// dial reaches a node as the client does, through the tunnel or the
	// proxy and TLS, for a connection the client cannot hold, as MONITOR.
	dial func(ctx context.Context, addr string) (net.Conn, error)

	// tlsState is whether the client uses TLS, as TLSState says it.
	tlsState string
}

// TLSState says whether the connection uses TLS, "TLS" or "no TLS": under
// prefer, as the server's answer to a first handshake decided.
func (k *KV) TLSState() string { return k.tlsState }

// OpenRedis connects to a Redis server, a cluster, or the master Sentinel
// names, and checks the connection.
func OpenRedis(ctx context.Context, cfg Config, knownHosts []string) (*KV, error) {
	if cfg.Engine != Redis {
		return nil, fmt.Errorf("%s is not Redis", cfg.Engine.Label())
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	addrs, err := cfg.RedisAddrs()
	if err != nil {
		return nil, err
	}
	// Every connection, to the first server as to a node a cluster
	// redirects to, goes the same way: through the SSH server when there is
	// one, which reaches the addresses the nodes announce.
	var tunnel *sshtunnel.Tunnel
	dialTCP := func(ctx context.Context, addr string) (net.Conn, error) {
		d := net.Dialer{Timeout: 15 * time.Second}
		return d.DialContext(ctx, "tcp", addr)
	}
	switch {
	case cfg.SSH.Enabled:
		if tunnel, err = sshtunnel.Connect(ctx, sshConfig(&cfg), knownHosts); err != nil {
			return nil, redact(err, cfg)
		}
		dialTCP = tunnel.Dial
	case cfg.Proxy.Kind != "":
		proxy := cfg.Proxy.dialer()
		dialTCP = func(ctx context.Context, addr string) (net.Conn, error) { return proxy(ctx, "tcp", addr) }
	}
	fail := func(err error) (*KV, error) {
		if tunnel != nil {
			tunnel.Close()
		}
		return nil, redact(err, cfg)
	}
	tc, err := tlsConfig(&cfg, cfg.Host)
	if err != nil {
		return fail(err)
	}
	if cfg.TLS == TLSPrefer && !speaksTLSVia(ctx, dialTCP, addrs[0], cfg.Host, tc) {
		tc = nil
	}
	dbIndex := 0
	if cfg.Database != "" {
		dbIndex, _ = strconv.Atoi(cfg.Database)
	}
	opt := rueidis.ClientOption{
		InitAddress: addrs,
		Username:    cfg.User,
		Password:    cfg.Password,
		SelectDB:    dbIndex,
		TLSConfig:   tc,
		DialCtxFn: func(ctx context.Context, addr string, _ *net.Dialer, tc *tls.Config) (net.Conn, error) {
			return dialNode(ctx, dialTCP, addr, tc)
		},
		ConnWriteTimeout:  30 * time.Second,
		BlockingPoolSize:  4,
		ClientName:        "dgopher",
		AlwaysRESP2:       true, // replies as redis-cli shows them
		DisableCache:      true, // a browser shows live values
		ForceSingleClient: cfg.Redis.Mode == RedisStandalone,
	}
	if cfg.Redis.Mode == RedisSentinel {
		opt.Sentinel = rueidis.SentinelOption{
			MasterSet: cfg.Redis.Master,
			Username:  cfg.Redis.SentinelUser,
			Password:  cfg.Redis.SentinelPassword,
			TLSConfig: tc,
			// A missed failover event would keep the client on the old
			// master: asking again now and then catches it.
			TopologyRefreshInterval: 5 * time.Second,
		}
	}
	pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	type dialed struct {
		client rueidis.Client
		err    error
	}
	ch := make(chan dialed, 1)
	// NewClient dials and handshakes without a context; it runs aside so
	// that the caller's context, or the 20 s limit, still ends the wait.
	go func() {
		client, err := rueidis.NewClient(opt)
		if err != nil {
			// rueidis returns a client even when the dial fails, which needs
			// closing, or a nil one of its own type, which Close would crash on.
			if client != nil && !reflect.ValueOf(client).IsNil() {
				client.Close()
			}
			client = nil
		}
		ch <- dialed{client, err}
	}()
	var client rueidis.Client
	select {
	case d := <-ch:
		if d.err != nil {
			return fail(d.err)
		}
		client = d.client
	case <-pctx.Done():
		go func() {
			if d := <-ch; d.client != nil {
				d.client.Close()
			}
		}()
		return fail(pctx.Err())
	}
	if cfg.Redis.Mode == RedisCluster && client.Mode() != rueidis.ClientModeCluster {
		client.Close()
		return fail(fmt.Errorf("%s is not a node of a cluster: choose a single server", addrs[0]))
	}
	if err = client.Do(pctx, client.B().Ping().Build()).Error(); err != nil {
		client.Close()
		return fail(err)
	}
	k := &KV{Config: cfg, Client: client, tunnel: tunnel, tlsState: tlsWord(tc != nil),
		dial: func(ctx context.Context, addr string) (net.Conn, error) { return dialNode(ctx, dialTCP, addr, tc) }}
	// Each COMMAND entry is [name, arity, flags, ...].
	if cmds, err := client.Do(pctx, client.B().Command().Build()).ToArray(); err == nil {
		k.readOnly = map[string]bool{}
		for _, c := range cmds {
			entry, err := c.ToArray()
			if err != nil || len(entry) < 3 {
				continue
			}
			name, _ := entry[0].ToString()
			flags, _ := entry[2].AsStrSlice()
			for _, f := range flags {
				if f == "readonly" {
					k.readOnly[strings.ToUpper(name)] = true
				}
			}
		}
	}
	return k, nil
}

// dialNode reaches a node by dialTCP, then TLS when tc is set, checking
// the node's certificate against its own name.
func dialNode(ctx context.Context, dialTCP func(context.Context, string) (net.Conn, error), addr string, tc *tls.Config) (net.Conn, error) {
	raw, err := dialTCP(ctx, addr)
	if err != nil || tc == nil {
		return raw, err
	}
	nc := tc.Clone()
	if host, _, err := net.SplitHostPort(addr); err == nil {
		nc.ServerName = host
	}
	conn := tls.Client(raw, nc)
	if err := conn.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	return conn, nil
}

// Close closes the connection and its tunnel.
func (k *KV) Close() error {
	k.Client.Close()
	if k.tunnel != nil {
		k.tunnel.Close()
	}
	return nil
}

// ServerVersion returns the server's version.
func (k *KV) ServerVersion(ctx context.Context) string {
	info, err := k.Client.Do(ctx, k.Client.B().Info().Section("server").Build()).ToString()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(info, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "redis_version:"); ok {
			return v
		}
	}
	return ""
}

// staticReadOnly lists the read-only commands of servers that hide
// COMMAND, as some hosted Redis do.
var staticReadOnly = map[string]bool{}

func init() {
	for _, c := range strings.Fields(`GET MGET STRLEN GETRANGE SUBSTR EXISTS TYPE TTL PTTL EXPIRETIME PEXPIRETIME
		HGET HMGET HGETALL HKEYS HVALS HLEN HEXISTS HSTRLEN HSCAN HRANDFIELD
		LRANGE LLEN LINDEX LPOS SMEMBERS SISMEMBER SMISMEMBER SCARD SRANDMEMBER SSCAN SINTER SUNION SDIFF SINTERCARD
		ZRANGE ZRANGEBYSCORE ZREVRANGE ZREVRANGEBYSCORE ZRANGEBYLEX ZREVRANGEBYLEX ZCARD ZSCORE ZMSCORE ZRANK ZREVRANK ZCOUNT ZLEXCOUNT ZSCAN ZRANDMEMBER ZINTER ZUNION ZDIFF
		XRANGE XREVRANGE XLEN XINFO XPENDING XREAD
		SCAN KEYS RANDOMKEY DBSIZE INFO PING ECHO TIME LASTSAVE OBJECT MEMORY DUMP
		BITCOUNT BITPOS GETBIT PFCOUNT GEOPOS GEODIST GEOHASH GEORADIUS_RO GEORADIUSBYMEMBER_RO GEOSEARCH
		SELECT CLIENT HELLO AUTH COMMAND LOLWUT ROLE SLOWLOG LATENCY`) {
		staticReadOnly[c] = true
	}
}

// readOnlySubcommands are the subcommands that only read, of the commands
// that also have some that change the server.
var readOnlySubcommands = map[string][]string{
	"CLIENT":  {"LIST", "INFO", "ID", "GETNAME", "TRACKINGINFO", "GETREDIR"},
	"SLOWLOG": {"GET", "LEN", "HELP"},
	"LATENCY": {"LATEST", "HISTORY", "DOCTOR", "GRAPH", "HISTOGRAM", "HELP"},
	"MEMORY":  {"USAGE", "STATS", "DOCTOR", "MALLOC-STATS", "HELP"},
	"CONFIG":  {"GET", "HELP"},
	"OBJECT":  {"ENCODING", "FREQ", "IDLETIME", "REFCOUNT", "HELP"},
	"COMMAND": {"", "COUNT", "DOCS", "INFO", "LIST", "GETKEYS", "GETKEYSANDFLAGS", "HELP"},
	"ACL":     {"WHOAMI", "CAT", "HELP"},
	"CLUSTER": {"INFO", "NODES", "SHARDS", "SLOTS", "MYID", "KEYSLOT", "COUNTKEYSINSLOT", "HELP"},
	"SCRIPT":  {"EXISTS", "HELP"},
	"XINFO":   {"STREAM", "GROUPS", "CONSUMERS", "HELP"},
}

// IsReadOnly reports whether a command, with its arguments, only reads.
func (k *KV) IsReadOnly(args ...string) bool {
	if len(args) == 0 {
		return true
	}
	cmd := strings.ToUpper(args[0])
	if subs, ok := readOnlySubcommands[cmd]; ok {
		sub := ""
		if len(args) > 1 {
			sub = strings.ToUpper(args[1])
		}
		for _, s := range subs {
			if s == sub {
				return true
			}
		}
		return false
	}
	switch cmd {
	case "PING", "INFO", "ROLE", "ECHO", "TIME", "DBSIZE", "LOLWUT":
		return true
	}
	if k.readOnly != nil {
		return k.readOnly[cmd]
	}
	return staticReadOnly[cmd]
}

// StatefulCommand says why a command cannot run on the pool of
// connections that a console and a key browser share, "" when it can: it
// would change one connection's state, which the next commands, sent on
// any connection, would not see, or would see by chance.
func StatefulCommand(args []string) string {
	if len(args) == 0 {
		return ""
	}
	switch strings.ToUpper(args[0]) {
	case "SELECT", "SWAPDB":
		return "SELECT would switch only one connection of the pool: open another connection with that database index instead."
	case "AUTH", "HELLO", "RESET", "QUIT":
		return strings.ToUpper(args[0]) + " changes the connection itself: edit the connection's settings instead."
	case "MULTI", "EXEC", "DISCARD", "WATCH", "UNWATCH":
		return "Transactions need one connection for all their commands, which this console does not hold: use a script (EVAL) instead."
	case "SUBSCRIBE", "PSUBSCRIBE", "SSUBSCRIBE", "MONITOR", "SYNC", "PSYNC":
		return strings.ToUpper(args[0]) + " takes the connection over, which this console does not support."
	case "CLIENT":
		if len(args) > 1 {
			switch strings.ToUpper(args[1]) {
			case "REPLY", "TRACKING", "SETNAME", "NO-EVICT", "NO-TOUCH":
				return "CLIENT " + strings.ToUpper(args[1]) + " changes one connection of the pool only."
			}
		}
	}
	return ""
}

// RiskOf says why a command deserves a confirmation on every environment,
// "" when it does not.
func RiskOf(args []string) string {
	if len(args) == 0 {
		return ""
	}
	switch strings.ToUpper(args[0]) {
	case "FLUSHALL":
		return "FLUSHALL deletes every key of every database on the server."
	case "FLUSHDB":
		return "FLUSHDB deletes every key of the database."
	case "KEYS":
		return "KEYS walks every key while the server waits, which stalls a large production server; the key browser uses SCAN instead."
	case "SHUTDOWN":
		return "SHUTDOWN stops the server."
	case "DEBUG":
		return "DEBUG can crash or stall the server."
	case "CONFIG":
		if len(args) > 1 && !strings.EqualFold(args[1], "GET") {
			return "CONFIG changes the server's configuration."
		}
	case "SWAPDB":
		return "SWAPDB exchanges two whole databases."
	case "SCRIPT", "FUNCTION":
		if len(args) > 1 && strings.EqualFold(args[1], "FLUSH") {
			return "This removes every script or function of the server."
		}
	case "CLIENT":
		if len(args) > 1 && strings.EqualFold(args[1], "KILL") {
			return "CLIENT KILL disconnects other clients."
		}
		if len(args) > 1 && strings.EqualFold(args[1], "PAUSE") {
			return "CLIENT PAUSE stops every client of the server."
		}
	case "REPLICAOF", "SLAVEOF":
		return "This changes the server's replication."
	case "MIGRATE":
		return "MIGRATE moves keys to another server."
	}
	return ""
}

// KeyInfo describes a key.
type KeyInfo struct {
	Key    string
	Type   string        // string, list, set, zset, hash, stream, or "none" when gone
	TTL    time.Duration // -1 without expiry
	Length int64         // characters of a string, items of the others
	Memory int64         // bytes, -1 when the server does not say
}

// ScanPos is where a scan of the keys is: on which of the masters, and
// at which cursor there. Its zero value starts a scan.
type ScanPos struct {
	Master int
	Cursor uint64
}

// Scan returns a batch of keys matching a glob pattern, the position to
// go on from, and whether every key was seen. Type, when not empty,
// limits the keys to one type (Redis 6+). A cluster's keys are on its
// masters, which a scan visits in turn.
func (k *KV) Scan(ctx context.Context, pos ScanPos, match, typ string, count int64) ([]string, ScanPos, bool, error) {
	if match == "" {
		match = "*"
	}
	masters, err := k.mastersFor(ctx, pos == ScanPos{})
	if err != nil {
		return nil, pos, false, err
	}
	if pos.Master >= len(masters) {
		return nil, pos, true, nil // the cluster lost a master since
	}
	c := masters[pos.Master]
	cmd := c.B().Scan().Cursor(pos.Cursor).Match(match).Count(count)
	var e rueidis.ScanEntry
	if typ != "" {
		e, err = c.Do(ctx, cmd.Type(typ).Build()).AsScanEntry()
	} else {
		e, err = c.Do(ctx, cmd.Build()).AsScanEntry()
	}
	if err != nil {
		return nil, pos, false, err
	}
	next := ScanPos{Master: pos.Master, Cursor: e.Cursor}
	if e.Cursor == 0 {
		next = ScanPos{Master: pos.Master + 1}
	}
	return e.Elements, next, next.Master == len(masters), nil
}

// DBSize counts the keys of the database: of every master of a cluster.
func (k *KV) DBSize(ctx context.Context) (int64, error) {
	masters, err := k.mastersFor(ctx, false)
	if err != nil {
		return 0, err
	}
	var n int64
	for _, c := range masters {
		size, err := c.Do(ctx, c.B().Dbsize().Build()).AsInt64()
		if err != nil {
			return 0, err
		}
		n += size
	}
	return n, nil
}

// mastersFor returns the clients of the servers holding the keys: the one
// client, or a cluster's masters, in the order of their addresses, read
// again when fresh is set.
func (k *KV) mastersFor(ctx context.Context, fresh bool) ([]rueidis.Client, error) {
	if k.Client.Mode() != rueidis.ClientModeCluster {
		return []rueidis.Client{k.Client}, nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.masters != nil && !fresh {
		return k.masters, nil
	}
	nodes := k.Client.Nodes()
	addrs := make([]string, 0, len(nodes))
	for addr := range nodes {
		addrs = append(addrs, addr)
	}
	sort.Strings(addrs)
	var masters []rueidis.Client
	for _, addr := range addrs {
		role, err := nodes[addr].Do(ctx, nodes[addr].B().Role().Build()).ToArray()
		if err != nil {
			return nil, fmt.Errorf("asking %s its role: %w", addr, err)
		}
		if len(role) > 0 {
			if r, _ := role[0].ToString(); r == "master" {
				masters = append(masters, nodes[addr])
			}
		}
	}
	if len(masters) == 0 {
		return nil, errors.New("the cluster has no master")
	}
	k.masters = masters
	return masters, nil
}

// Info returns what is known of a key.
func (k *KV) Info(ctx context.Context, key string) (KeyInfo, error) {
	info := KeyInfo{Key: key, Memory: -1}
	b := k.Client.B()
	typ, err := k.Client.Do(ctx, b.Type().Key(key).Build()).ToString()
	if err != nil {
		return info, err
	}
	info.Type = typ
	if typ == "none" {
		return info, nil
	}
	cmds := rueidis.Commands{b.Pttl().Key(key).Build(), b.MemoryUsage().Key(key).Build()}
	switch typ {
	case "string":
		cmds = append(cmds, b.Strlen().Key(key).Build())
	case "list":
		cmds = append(cmds, b.Llen().Key(key).Build())
	case "set":
		cmds = append(cmds, b.Scard().Key(key).Build())
	case "zset":
		cmds = append(cmds, b.Zcard().Key(key).Build())
	case "hash":
		cmds = append(cmds, b.Hlen().Key(key).Build())
	case "stream":
		cmds = append(cmds, b.Xlen().Key(key).Build())
	case "vectorset":
		cmds = append(cmds, b.Arbitrary("VCARD").Keys(key).Build())
	case "array":
		cmds = append(cmds, b.Arbitrary("ARCOUNT").Keys(key).Build())
	}
	res := k.Client.DoMulti(ctx, cmds...) // each command keeps its own error
	if ms, err := res[0].AsInt64(); err == nil {
		info.TTL = time.Duration(ms) * time.Millisecond
		if ms < 0 {
			info.TTL = -1
		}
	}
	if m, err := res[1].AsInt64(); err == nil {
		info.Memory = m
	}
	if len(res) > 2 {
		info.Length, _ = res[2].AsInt64()
	}
	return info, nil
}

// Do runs a command, as typed in a console.
func (k *KV) Do(ctx context.Context, args []string) (any, error) {
	if len(args) == 0 {
		return nil, errors.New("no command")
	}
	cmd := k.Client.B().Arbitrary(args[0]).Args(args[1:]...)
	var v any
	var err error
	if isBlocking(args) {
		v, err = k.Client.Do(ctx, cmd.Blocking()).ToAny()
	} else {
		v, err = k.Client.Do(ctx, cmd.Build()).ToAny()
	}
	if rueidis.IsRedisNil(err) {
		return nil, nil
	}
	return v, err
}

// isBlocking reports whether a command can wait on the server; it then
// takes a connection of its own, as the shared one would hold every other
// command behind it.
func isBlocking(args []string) bool {
	switch strings.ToUpper(args[0]) {
	case "BLPOP", "BRPOP", "BLMOVE", "BRPOPLPUSH", "BZPOPMIN", "BZPOPMAX", "BLMPOP", "BZMPOP", "WAIT", "WAITAOF":
		return true
	case "XREAD", "XREADGROUP":
		for _, a := range args[1:] {
			if strings.EqualFold(a, "BLOCK") {
				return true
			}
		}
	}
	return false
}

// SplitCommand splits a console line into arguments as redis-cli does:
// by spaces, with "double" quotes (and their backslash escapes) and
// 'single' quotes keeping spaces in one argument.
func SplitCommand(line string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inArg := false
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '"' || r == '\'':
			quote := r
			inArg = true
			closed := false
			for i++; i < len(rs); i++ {
				c := rs[i]
				if c == quote {
					closed = true
					break
				}
				if c == '\\' && quote == '"' && i+1 < len(rs) {
					i++
					switch rs[i] {
					case 'n':
						cur.WriteRune('\n')
					case 't':
						cur.WriteRune('\t')
					case 'r':
						cur.WriteRune('\r')
					case 'x':
						if i+2 < len(rs) {
							if b, err := strconv.ParseUint(string(rs[i+1:i+3]), 16, 8); err == nil {
								cur.WriteByte(byte(b))
								i += 2
								continue
							}
						}
						cur.WriteRune('x')
					default:
						cur.WriteRune(rs[i])
					}
					continue
				}
				cur.WriteRune(c)
			}
			if !closed {
				return nil, errors.New("unbalanced quotes")
			}
			if i+1 < len(rs) && !unicode.IsSpace(rs[i+1]) {
				return nil, errors.New("a closing quote must be followed by a space")
			}
		case unicode.IsSpace(r):
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			inArg = true
			cur.WriteRune(r)
		}
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args, nil
}

// CommandLine is a command of a file of commands, with its line.
type CommandLine struct {
	Line int
	Args []string
}

// SplitCommands reads a file of commands, one a line as the console takes
// them; empty lines and those starting with # are passed over.
func SplitCommands(text string) ([]CommandLine, error) {
	var out []CommandLine
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		args, err := SplitCommand(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		out = append(out, CommandLine{Line: i + 1, Args: args})
	}
	return out, nil
}

// FormatReply writes a reply as redis-cli shows it.
func FormatReply(v any) string {
	var b strings.Builder
	formatReply(&b, v, "")
	return strings.TrimRight(b.String(), "\n")
}

func formatReply(b *strings.Builder, v any, indent string) {
	switch x := v.(type) {
	case nil:
		b.WriteString("(nil)\n")
	case int64:
		fmt.Fprintf(b, "(integer) %d\n", x)
	case string:
		b.WriteString(strconv.Quote(x) + "\n")
	case []any:
		if len(x) == 0 {
			b.WriteString("(empty array)\n")
			return
		}
		w := len(strconv.Itoa(len(x)))
		for i, item := range x {
			if i > 0 {
				b.WriteString(indent)
			}
			prefix := fmt.Sprintf("%*d) ", w, i+1)
			b.WriteString(prefix)
			formatReply(b, item, indent+strings.Repeat(" ", len(prefix)))
		}
	case error:
		b.WriteString("(error) " + x.Error() + "\n")
	default:
		fmt.Fprintf(b, "%v\n", x)
	}
}
