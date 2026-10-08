# AGENTS.md

DGopher is a native database client for PostgreSQL, MySQL, ClickHouse,
SQLite, DuckDB and Redis: one Go binary whose UI is drawn by
[MyGo](https://mygo.egoist.dev), without a webview.

**Read [docs/development.md](docs/development.md) for the package layout and
[docs/safety.md](docs/safety.md) before changing anything that runs
statements.**

## Hard rules

- **Every statement goes through `internal/safety`** before it reaches the
  server. A statement it cannot classify counts as a write.
- **Grid edits apply in one transaction**, and each statement must change
  exactly one row by primary key, or everything rolls back.
- **Secrets never reach project files, logs or the audit log.** Pass shown or
  logged text through `internal/redact`.
- **An open transaction is never closed silently.** Closing a tab,
  disconnecting or quitting asks first.
- **Engine behavior is not only in `Dialect`.** Switches on `db.Engine` are
  spread across packages; search `case db.<Engine>` when changing an engine.
- **DuckDB and in-memory SQLite share one connection** (`DB.single`), so all
  their sessions share one transaction.

## Commands

cgo is required (DuckDB). `nix develop` provides Go, the GUI libraries and
`CGO_ENABLED=1`.

```sh
CGO_ENABLED=1 go run .                        # run the app
CGO_ENABLED=1 go tool mygo build              # bundle and installers into build/ (mygo.json)
go vet ./... && gofmt -l .                    # both clean today; any output is new
go test ./...                                 # unit and headless UI tests
go test ./internal/sqltext -run TestSplit     # one test
DGOPHER_IT=1 go test ./...                   # also against the integration servers
DGOPHER_SNAPSHOTS=/tmp/snaps go test ./...   # write PNGs of the UI under test
```

Run vet, gofmt and `go test ./...` before calling a change done, and the
integration tests too when it touches `internal/db`.

## Conventions

- Integration servers are Docker containers `dbgopher-pg`, `dbgopher-mysql`,
  `dbgopher-ch`, `dbgopher-redis`, `dbgopher-redis-cluster` and
  `dbgopher-redis-sentinel` (`docs/development.md` starts them); ports and
  passwords are in `internal/testutil/testutil.go` and, for Redis's,
  `internal/db/integration_test.go`. Tests call `testutil.Integration(t)`, which
  serializes them on Unix; elsewhere use `go test -p 1`.
- UI tests run headless through MyGo's `ui.Tester`, with `dataview.FakeHost`
  as the window and `testutil.WaitFor` to wait for frames.
- MyGo is replaced in `go.mod` by the `dbgopher` branch of `saanuregh/mygo`;
  an API missing upstream may exist there. Updating it is described in
  `docs/development.md`.
- `DGOPHER_CONFIG_DIR` points manual runs away from the real config.
- `build/`, `.mygo/` and `./dgopher` are build outputs; leave them out of
  searches.
