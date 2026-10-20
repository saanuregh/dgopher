# Development

This page is for people working on DGopher's code: how to build it, test
it, release it, and find your way around the packages.
[Architecture](architecture.md) explains how the packages work together,
and [Testing](testing.md) how to write tests.

Read [Safety model](safety.md) before changing anything that runs
statements.

- [Set up](#set-up)
- [Build and run](#build-and-run)
- [Test](#test)
- [Release](#release)
- [MyGo](#mygo)
- [agent-cli-connector](#agent-cli-connector)
- [Third-party licenses](#third-party-licenses)
- [Code layout](#code-layout)

## Set up

You need:

- **Go**, at the version in `go.mod`. An older `go` downloads the right
  one automatically.
- **A C compiler.** cgo is required: DuckDB is compiled in through
  [duckdb-go](https://github.com/duckdb/duckdb-go), which links it
  statically. Set `CGO_ENABLED=1`.
- **On Linux,** GTK 3 and OpenGL for the window. WebKitGTK is not needed.

With Nix on Linux, `nix develop` gives a shell with Go, the GUI libraries
the window loads, and `CGO_ENABLED=1` (see `flake.nix`). The flake covers
`x86_64-linux` and `aarch64-linux` only.

## Build and run

```sh
CGO_ENABLED=1 go run .                  # run the app
CGO_ENABLED=1 go build -o dgopher .     # build a single binary
CGO_ENABLED=1 go tool mygo build        # app bundle, installers and archives in build/ (configured by mygo.json)
```

To keep a development run's settings and window layout apart from your
own, set `DGOPHER_CONFIG_DIR` to a scratch directory. `DGOPHER_DATA_DIR`
moves only the layout. See [Where things are kept](storage.md).

### Other platforms

With cgo, building for macOS or Windows needs a C toolchain for that
platform, or a machine running it.

For Windows, `zig cc` is enough to vet:

```sh
GOOS=windows GOARCH=amd64 CC="zig cc -target x86_64-windows-gnu" go vet ./...
```

It cannot link DuckDB, whose Windows library needs MinGW's C++ library.
Build on Windows instead.

## Test

```sh
go vet ./... && gofmt -l .                   # both print nothing on a clean tree
go test ./...                                # unit tests and headless UI tests
go test ./internal/sqltext -run TestSplit    # one test
docker compose up -d --wait                  # start the integration servers (below)
DGOPHER_IT=1 go test ./...                   # also against those servers
DGOPHER_SNAPSHOTS=/tmp/snaps go test ./...   # write PNGs of the UI as the tests see it
```

Run vet, gofmt and `go test ./...` before calling a change done. Run the
integration tests too when the change touches `internal/db`.

[Testing](testing.md) explains how to write each kind of test.

### Integration servers

Integration tests run only with `DGOPHER_IT=1`. They call
`testutil.Integration(t)`, which serializes them across packages on Unix;
elsewhere, add `-p 1`. SQLite and DuckDB need no server.

[`compose.yaml`](../compose.yaml) runs every server the tests use:
PostgreSQL, MySQL, ClickHouse, Redis 7 and 8, a Redis cluster, and a Redis
master with a sentinel. Their ports and passwords match
`internal/testutil/testutil.go`, `internal/db/integration_test.go` and,
for Redis 8, `internal/db/redismodules_test.go` and
`internal/ui/redis/redis_test.go`.

```sh
docker compose up -d --wait    # start them, and wait until they are ready
DGOPHER_IT=1 go test ./...     # run every test against them
docker compose down            # stop and remove them
```

The Redis cluster and sentinel use the host's network, so the addresses
the nodes announce are reachable from the tests. This needs Docker on
Linux, or Docker Desktop with host networking turned on.

### Screenshots

`TestScreenshots` draws the screenshots in the README and these docs, in
the dark theme (and the main window once in light), from the sample shop
and connections made up around it.
Draw them again after a change to what they show. Text needs Pango's
library on the loader's path, which `nix develop` provides.

The query plan, Server Activity and Redis images need the integration
servers (`docker compose up -d --wait`), so they come only with
`DGOPHER_IT`. Without it, `TestScreenshotsServers` skips and leaves them as
they are.

```sh
DGOPHER_IT=1 DGOPHER_SCREENSHOTS=$PWD/docs/images go test ./internal/app -run TestScreenshots
```

## Release

Pushing a tag `v<version>` builds and publishes a release
(`.github/workflows/release.yml`):

1. Set `"version"` in `mygo.json` and commit. The workflow checks that the
   tag matches it.
2. Tag and push:

   ```sh
   git tag v0.2.0 && git push origin main v0.2.0
   ```

Each platform builds on a runner of its own system, because DuckDB needs
cgo. Each uploads its installers: the macOS disk image, the Windows
installer, and the Linux Debian package and archive. The release is
published, with a `SHA256SUMS` of every file, once every platform's files
are there.

`.github/workflows/ci.yml` runs gofmt, vet and the tests on every push and
pull request, on Linux and Windows. It does not run the integration tests.

## MyGo

`go.mod` replaces MyGo with a commit of a `dbgopher-v<version>` branch of
[saanuregh/mygo](https://github.com/saanuregh/mygo/branches/all?query=dbgopher).
The branch is named for the MyGo release it is based on, with `-2`, `-3`
and so on when it is rebuilt on the same release.

The branch is upstream MyGo plus the changes DGopher needs that upstream
does not have yet:

- the commits of its open pull requests to
  [egoist/mygo](https://github.com/egoist/mygo/pulls?q=author%3Asaanuregh),
  as they were sent;
- frozen table columns;
- the code editor's `GutterButtons` and `OnAcceptCompletion`;
- the code editor's `LinkAt` and `OnLink`, which underline a name while
  ⌘ is held and open it on a click.

An API missing from upstream MyGo may exist there.

To move to a new MyGo release:

1. Create a new branch from the release and apply those commits, dropping
   the ones upstream has taken. Make a new branch rather than rewriting the
   old one, so the commit `go.mod` pins now stays reachable.
2. Point `go.mod` at the new commit:

   ```sh
   go mod edit -replace=github.com/egoist/mygo=github.com/saanuregh/mygo@<commit> && go mod tidy
   ```

## agent-cli-connector

The AI chat drives `claude`, `codex` and `pi` through
[agent-cli-connector](https://github.com/saanuregh/agent-cli-connector).
Until it is published, `go.mod` replaces it with a local checkout at
`/home/sr/projects/personal/agent-cli-connector`; the build needs that
checkout, and CI cannot fetch it. Once it is published, drop the replace:

```sh
go mod edit -dropreplace=github.com/saanuregh/agent-cli-connector && go get github.com/saanuregh/agent-cli-connector@latest && go mod tidy
```

The chat's tests use the connector's scripted `agenttest` provider. To try
the real command lines, which must be installed and signed in, and which
spend tokens:

```sh
DGOPHER_LIVE_CHAT=claude,codex,pi go test ./internal/ui/chat -run TestLiveChat -v
```

## Third-party licenses

Every bundle and installer ships `LICENSE` and
`resources/THIRD_PARTY_NOTICES.txt`. The notices hold the license files of
every module linked in on any platform DGopher ships for, and of the code
DuckDB compiles in (kept in `tools/licenses/duckdb.txt`).

```sh
go run ./tools/licenses    # rewrite resources/THIRD_PARTY_NOTICES.txt; run after changing go.mod
go run ./tools/icon        # redraw resources/icon.png
```

When `duckdb-go-bindings` moves to a new DuckDB release, the licenses tool
stops until `duckdb.txt` is rewritten from that release's source:

```sh
go run ./tools/licenses -duckdb <DuckDB source directory>
```

## Code layout

`main.go` starts the app. Everything else is under `internal/` and
`tools/`.

### Window and tabs

| Path | What |
| --- | --- |
| `internal/app` | the window: navigator, tabs, menus, palette, dialogs, settings, and the quit flow |
| `internal/ui/widgets` | the theme, icons, small controls, the confirm dialog, and the tab contract: what a tab may say about itself (icon, places, holding its connection, coming into view) |
| `internal/ui/panes` | a window's editor groups: the tree of splits, the focused group, and the neighbour in a direction |
| `internal/ui/tablist` | a group's tabs: their order, pins and the one in front, changed only through its methods |
| `internal/ui/editor` | the SQL editor, including Vim keys |
| `internal/ui/query` | SQL editor tabs: running statements and scripts, plans, parameters, and the notebook view |
| `internal/ui/dataview` | the results grid and its panels, table tabs, export, charts, the query builder and the ER diagram |
| `internal/ui/dashboard` | dashboard tabs: panels of reads shown as charts, rows or values, with parameters and refresh, kept in a project's file |
| `internal/ui/modelview` | data model tabs: a model's tables, its DDL, and its comparison with a database or another model |
| `internal/ui/redis` | Redis tabs: the key tree, values and their editing, the console, and the monitor, slow log, memory and Pub/Sub panels |
| `internal/ui/chat` | a SQL editor's AI chat: an agent's session through agent-cli-connector, its transcript, its model and effort, and the app's tools it may call |
| `internal/keymap` | the actions that have keys, their default keys, keymap.json, and keys of two in a row |

The tab packages (`dataview`, `query`, `redis`, `dashboard`, `modelview`) and `chat`
each declare a small `Host` interface for what they need from the window.
`internal/app` implements all of them.

### Databases and SQL

| Path | What |
| --- | --- |
| `internal/db` | connections, sessions, cursors, each engine's dialect, generated edits, and the Redis client |
| `internal/connection` | a connection's live state: its pool, sessions, status, idle limits, and the schema read so far |
| `internal/safety` | the safety policy: which statements are refused or need a confirmation |
| `internal/sqltext` | the SQL lexer: highlighting, splitting, classification for the safety policy, completion context, and the formatter ([How SQL is read](sql.md)) |
| `internal/params` | query parameters: finding, typing and binding them |
| `internal/sqlfile` | reads the statements of a SQL file as it streams, such as a dump, `COPY` rows included |
| `internal/redact` | hides secrets in text that is shown or logged |

Engine behavior is not only in `db.Dialect`. Switches on `db.Engine` are
spread across packages; search for `case db.<Engine>` when changing an
engine.

### Data in and out

| Path | What |
| --- | --- |
| `internal/export` | CSV, TSV, JSON, JSON Lines, SQL, Markdown and Excel writers, and Parquet and DuckDB files through DuckDB. Text formats are written in Go, clipboard copies too, so a copy and a file of the same rows agree. |
| `internal/fileimport` | reads files to import: CSV, JSON and Parquet through DuckDB; Excel and XML loaded into it |
| `internal/datamodel` | data models: a schema's tables kept in a project file, written as DDL for any engine, compared, and the migration between two |
| `internal/schemadoc` | the script that creates a schema's objects in an order that runs, and its documentation as HTML or Markdown |
| `internal/testdata` | makes up the values of generated rows, and suggests each column's generator from its type and name |
| `internal/decode` | turns values stored compressed or serialized into text: gzip, zlib, zstd, LZ4, Snappy, Brotli, MessagePack, pickle, PHP, Java and Protobuf |

### Storage, projects and security

| Path | What |
| --- | --- |
| `internal/project` | project folders and their shared connections and snippets (`dgopher.json`) |
| `internal/state` | SQLite state: a project's `state.sqlite` (open tabs, query history, filters, row colors, the audit log) and the app's `ui.sqlite` (window layout) |
| `internal/audit` | the hash-chained audit log: append, traffic batching, read, verify |
| `internal/store` | the config directory's JSON files and keychain secrets |
| `internal/settings` | the user's settings and how values are shown |
| `internal/secretcmd` | runs a password command without a shell |
| `internal/sshtunnel` | SSH port forwarding and dialing through SSH, with host key checks; `sshtest` is an SSH server for tests |
| `internal/netproxy` | SOCKS5 and HTTP CONNECT proxies, and a local port forwarded through one; `proxytest` runs both for tests |

### Helpers and tools

| Path | What |
| --- | --- |
| `internal/lists` | generic slice helpers |
| `internal/testutil` | shared test helpers: snapshots, waiting for frames, the integration servers, and the open-file check; `serverlock` serializes integration tests |
| `tools/icon` | draws `resources/icon.png` |
| `tools/licenses` | writes `resources/THIRD_PARTY_NOTICES.txt` |
