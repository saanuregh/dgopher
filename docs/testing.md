# Testing

How to write DGopher's tests. [Development](development.md#test) has the
commands to run them and the servers the integration tests need.

- [Kinds of tests](#kinds-of-tests)
- [Conventions](#conventions)
- [UI tests](#ui-tests)
- [Integration tests](#integration-tests)
- [Test helpers](#test-helpers)

## Kinds of tests

| Kind | Runs | Needs |
| --- | --- | --- |
| Unit | always, with `go test ./...` | nothing |
| UI | always; drawn without a window | nothing; SQLite and DuckDB run in memory |
| Integration | only with `DGOPHER_IT=1` | the servers in [`compose.yaml`](../compose.yaml): `docker compose up -d --wait` |
| Fuzz | with `-fuzz` | nothing |
| Screenshots | only with `DGOPHER_SCREENSHOTS` | Pango's library on the loader's path |

Prefer a UI test on in-memory SQLite or DuckDB over an integration test when
the behavior does not depend on a server.

## Conventions

- **Name tests for the behavior,** in plain words, with a comment that
  states it as a sentence:

  ```go
  // ⌘↵ runs the statement around the caret, which blank lines delimit.
  func TestRunsStatementAtCaretAcrossBlankLines(t *testing.T) {
  ```

  Integration tests in `internal/db` are named `TestIntegration…`.
- **Table-driven** tests use an anonymous struct slice, with `c` as the
  loop variable:

  ```go
  for _, c := range []struct {
      e   db.Engine
      sql string
      out bool
  }{
      {db.Postgres, "VACUUM ANALYZE shop.orders", true},
      {db.MySQL, "OPTIMIZE TABLE t", false},
  } {
      if got := OutsideTransaction(c.e, c.sql); got != c.out {
          t.Errorf("%s %q: %v", c.e, c.sql, got)
      }
  }
  ```

- **No assertion library.** Fail with `t.Errorf` or `t.Fatalf`, printing what
  was got.
- **Files** go in `t.TempDir()`. Close every file a test opens: see
  [the open-file check](#the-open-file-check).
- **Secrets** use `store.MemorySecrets()`, never the real keychain.
- **Config:** pass `t.TempDir()` to `store.Open` rather than setting
  `DGOPHER_CONFIG_DIR`.

## UI tests

UI tests draw frames headlessly with MyGo's `ui.Tester`.
`dataview.FakeHost` (`internal/ui/dataview/fakehost.go`) stands in for the
app window: it makes a project in a temporary folder, runs posted and
background work, and records confirmations, errors and jobs for you to
check. On cleanup it closes tabs, pools and state in the order quitting
does.

The SQL editor's tests wrap it in `fakeQueryHost`
(`internal/ui/query/host_test.go`), with helpers that open a connected
editor:

```go
func openEditor(t *testing.T, cfg db.Config, w, h int, text string) (*fakeQueryHost, *ui.Tester, *connection.Conn, *Tab) {
	t.Helper()
	a := newFakeQueryHost(t)
	cn := a.AddConn(cfg)
	tt := ui.NewTester(a.view, w, h)
	return a, tt, cn, newEditor(t, a, tt, cn, text)
}
```

A test then acts and waits for the frames to settle:

```go
func TestRunsStatementAtCaretAcrossBlankLines(t *testing.T) {
	a, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"},
		1000, 700, "SELECT 1 AS one\n\nSELECT 2 AS two\nUNION ALL SELECT 3")
	testutil.SetCaret(tt, &q.Editor, strings.Index(q.Editor.Text(), "UNION"))
	q.Run(RunStatement)
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running })
	// check q.results …
}
```

- **Input:** `tt.Type("…")`, `tt.Key(ui.Cmd, ui.KeyS)`, `tt.Click("Commit")`,
  and `tt.Frame()` to draw one frame.
- **Waiting:** `testutil.WaitFor(t, tt, what, cond)` draws frames every
  20 ms until `cond` holds, and fails after 20 seconds, printing the texts
  on screen.
- **Checking text:** `tt.Texts()`, `testutil.HasTextContaining` and
  `testutil.Settled`.
- **App-level tests** in `internal/app` build a real `App` with
  `newBareApp(t)` and draw it with `ui.NewTester(a.windowView(a.main), w, h)`.

## Integration tests

Start the servers once, then run the tests against them:

```sh
docker compose up -d --wait
DGOPHER_IT=1 go test ./...                                  # everything
DGOPHER_IT=1 go test ./internal/db -run TestIntegration     # one package's
docker compose down                                         # when done
```

Start an integration test with `testutil.Integration(t)`. It skips the test
unless `DGOPHER_IT` is set, and takes a lock so integration tests from
different packages do not run against the servers at once:

```go
func TestAutocomplete(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	a, tt, cn, q := openEditor(t, testutil.PGConfig(), 1200, 760, "")
	// …
}
```

- `testutil.PGConfig()` returns the PostgreSQL test server's config, and
  `testutil.SeedPostgres(t)` recreates its `shop` schema. Other engines'
  configs are defined in the tests that use them.
- `internal/db` cannot import `testutil`, which imports it, so it has its own
  `integration(t)` helper and a table of fixtures, one per engine, in
  `internal/db/integration_test.go`. Its tests loop over the fixtures with
  `t.Run(f.cfg.Name, …)`.
- The lock is a file lock on Unix (`internal/testutil/serverlock`). On other
  systems it does nothing, so run integration tests with `go test -p 1`.
- **A new server**, or a new engine version, goes in `compose.yaml`, with
  its port and password in `internal/testutil/testutil.go` or the test that
  uses it. Bind its ports to `127.0.0.1`, and add a health check so
  `docker compose up --wait` waits for it.

## Test helpers

### The open-file check

Packages whose tests use files run their `TestMain` through `testutil.Main`:

```go
func TestMain(m *testing.M) { os.Exit(testutil.Main(m)) }
```

On Linux, it fails the package if a file is still open in a test's
temporary folder after the tests end, because Windows could not remove
it. Add this `TestMain` to a new package whose tests open files.

### In-process servers

- `sshtest.Start` (`internal/sshtunnel/sshtest`) runs an SSH server for the
  test.
- `proxytest.SOCKS5` and `proxytest.HTTP` (`internal/netproxy/proxytest`)
  run proxies and return their `netproxy.Config`.

### Snapshots and screenshots

- `testutil.Snapshot(t, tt, name)` writes `<name>.png` of the current frame
  when `DGOPHER_SNAPSHOTS` names a folder, and does nothing otherwise. Use
  it to see what a UI test sees.
- `TestScreenshots` (`internal/app/screenshots_test.go`) draws the
  screenshots used in the README and docs; see
  [Screenshots](development.md#screenshots).

### Accessibility check

`TestControlsAreNamed` (`a11y_test.go` at the repository root) parses every
file under `internal/` and fails if a MyGo input control (`TextInput`,
`Select`, `NumberInput`, `Switch`, and others) has no `Label` and is not
the first control in a `ui.Field`. Give a new control a label to pass it.

### Fuzz and benchmarks

| Fuzz test | Package |
| --- | --- |
| `FuzzTokenize` | `internal/sqltext`: tokens cover the text; splitting, classifying and formatting hold up |
| `FuzzDecode` | `internal/decode` |
| `FuzzVim` | `internal/ui/editor` |

```sh
go test ./internal/sqltext -run '^$' -fuzz '^FuzzTokenize$' -fuzztime 60s
go test ./internal/sqltext -run '^$' -bench Tokenize -benchmem
```

Benchmarks also cover redaction, cell text, DuckDB transaction state and
literal formatting.
