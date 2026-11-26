# AGENTS.md

DGopher is a native database client for PostgreSQL (and CockroachDB,
YugabyteDB, TimescaleDB), MySQL (and MariaDB, TiDB), SQL Server, Oracle,
ClickHouse, SQLite, DuckDB and Redis: one Go binary whose UI is drawn by
[MyGo](https://mygo.egoist.dev), without a webview.

**Read [docs/development.md](docs/development.md) for the package layout,
[docs/architecture.md](docs/architecture.md) for how the packages work
together, and [docs/safety.md](docs/safety.md) before changing anything that
runs statements.** [docs/testing.md](docs/testing.md) explains how to write
tests.

## Hard rules

- **Every statement goes through `internal/safety`** before it reaches the
  server. A statement it cannot classify counts as a write.
- **Grid edits apply in one transaction**, and each statement must change
  exactly one row by primary key, or everything rolls back.
- **Secrets never reach project files, logs or the audit log.** Pass shown or
  logged text through `internal/redact`.
- **An open transaction is never closed silently.** Closing a tab,
  disconnecting or quitting asks first. Only when the system ends the
  session and will not wait, as at logout on Windows, does it roll back
  without asking, in the audit log.
- **Engine behavior lives in each engine's rules**: a type per engine
  implements `engineRules`, and SQL engines `SQLRules` (both declared in
  `internal/db/engine.go`; each engine's methods are in its own file, such
  as `internal/db/postgres.go`), so an engine missing a hook does not
  compile. Engines come in families (`Engine.Kind()`): outside
  `internal/db`, reach the rules through `Engine` methods and the family's
  entry point, `db.SQL(e)` or `db.KeyValue(e)`, whose `false` the caller
  handles. A connection holds one `db.Store` (`Conn.Store`, read as
  `Conn.DB()` or `Conn.KV()`). Do not compare with an engine outside the
  reviewed list in `TestNoEngineSwitches`, which fails on a new
  comparison. An engine built on another's type (as MariaDB on MySQL's)
  declares its own `Engine()`, and a dialect type of its own embedding
  the parent's, whose `Engine()` is its own, returned by its `dialect()`:
  rules are found through a `Dialect` too.
- **DuckDB and in-memory SQLite share one connection** (`DB.single`), so all
  their sessions share one transaction.

## Commands

cgo is required (DuckDB). `go run ./tools/dev` runs the contributor
commands on any OS and sets `CGO_ENABLED=1` itself; `docs/development.md`
has each OS's setup. `nix develop` provides Go, the GUI libraries,
`pg_dump` for the backup tests, the compose providers and `CGO_ENABLED=1`.

```sh
go run ./tools/dev doctor                     # what the build and tests need, and what is missing
go run ./tools/dev run                        # run the app (--scratch: throwaway settings)
CGO_ENABLED=1 go tool mygo build              # bundle and installers into build/ (mygo.json)
go run ./tools/dev check                      # gofmt, go vet and go test ./...; all clean today
go run ./tools/dev test -- ./internal/sqltext -run TestSplit   # one test
go run ./tools/dev servers up                 # start the integration servers (Podman, else Docker)
go run ./tools/dev test --it                  # also against the integration servers
go run ./tools/dev test --snapshots           # write PNGs of the UI under test
DGOPHER_LIVE_CHAT=claude go test ./internal/ui/chat -run TestLiveChat   # the AI chat against a real CLI; spends tokens
```

Run `go run ./tools/dev check` before calling a change done, and the
integration tests too when it touches `internal/db`.

## Conventions

- Integration servers are the containers of `compose.yaml`: `dgopher-pg`,
  `dgopher-mysql`, `dgopher-ch`, `dgopher-redis`, `dgopher-redis8`,
  `dgopher-redis-cluster` and `dgopher-redis-sentinel`, and, started with
  `go run ./tools/dev servers up --profile compat`, `dgopher-mariadb`,
  `dgopher-tidb`, `dgopher-cockroachdb`, `dgopher-yugabytedb`,
  `dgopher-timescaledb`, `dgopher-valkey`, `dgopher-valkey-cluster` and
  `dgopher-valkey-sentinel`, and, with `--profile enterprise`,
  `dgopher-sqlserver` and `dgopher-oracle`; their tests skip when they are not running. Their ports and passwords are in
  `internal/testutil/testutil.go`,
  `internal/db/integration_test.go`, for the profiled servers
  `internal/testutil/optionalserver/optionalserver.go`, and, for Redis and
  Valkey, `internal/testutil/redisserver`; keep `compose.yaml` matching them.
  `go run ./tools/dev test --it --redis valkey` runs the Redis tests against
  Valkey's servers.
  Tests call `testutil.Integration(t)`, which serializes them on Unix;
  elsewhere use `go test -p 1`, which `go run ./tools/dev test --it` adds.
  `internal/containercli` chooses Podman, else Docker
  (`DGOPHER_CONTAINER` overrides); name images in full in `compose.yaml`.
- UI tests run headless through MyGo's `ui.Tester`, with `dataview.FakeHost`
  as the window and `testutil.WaitFor` to wait for frames.
- MyGo is replaced in `go.mod` by a commit of a `dbgopher-v<version>` branch of
  `saanuregh/mygo`; an API missing upstream may exist there. Updating it is
  described in `docs/development.md`.
- `DGOPHER_CONFIG_DIR` points manual runs away from the real config.
- A local agent-cli-connector replace in `go.mod` must not be committed; see
  `docs/development.md`.
- `build/`, `.mygo/` and `./dgopher` are build outputs; leave them out of
  searches.
