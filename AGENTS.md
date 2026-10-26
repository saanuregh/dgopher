# AGENTS.md

DGopher is a native database client for PostgreSQL, MySQL, ClickHouse,
SQLite, DuckDB and Redis: one Go binary whose UI is drawn by
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
  disconnecting or quitting asks first.
- **Engine behavior lives in each engine's rules**: a type per engine
  implements `engineRules`, and SQL engines `sqlRules` (both declared in
  `internal/db/engine.go`; each engine's methods are in its own file, such
  as `internal/db/postgres.go`), so an engine missing a hook does not
  compile. Do not compare with an engine outside the reviewed list in
  `TestNoEngineSwitches`, which fails on a new comparison. An engine built
  on another's type also needs a dialect type of its own whose `Engine()`
  is its own, returned by its `dialect()`: rules are found through a
  `Dialect` too.
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
docker compose up -d --wait                  # start the integration servers (compose.yaml)
DGOPHER_IT=1 go test ./...                   # also against the integration servers
DGOPHER_SNAPSHOTS=/tmp/snaps go test ./...   # write PNGs of the UI under test
DGOPHER_LIVE_CHAT=claude go test ./internal/ui/chat -run TestLiveChat   # the AI chat against a real CLI; spends tokens
```

Run vet, gofmt and `go test ./...` before calling a change done, and the
integration tests too when it touches `internal/db`.

## Conventions

- Integration servers are the containers of `compose.yaml`: `dgopher-pg`,
  `dgopher-mysql`, `dgopher-ch`, `dgopher-redis`, `dgopher-redis8`,
  `dgopher-redis-cluster` and `dgopher-redis-sentinel`. Their ports and
  passwords are in `internal/testutil/testutil.go`,
  `internal/db/integration_test.go` and, for Redis 8,
  `internal/db/redismodules_test.go` and `internal/ui/redis/redis_test.go`; keep `compose.yaml` matching them.
  Tests call `testutil.Integration(t)`, which serializes them on Unix;
  elsewhere use `go test -p 1`.
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
