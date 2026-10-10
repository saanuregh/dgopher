# Development

## Build and run

You need Go 1.27.2 or later, which `go` downloads automatically, and a C
compiler: DuckDB is built in through the cgo driver
[duckdb-go](https://github.com/duckdb/duckdb-go), which links it
statically.

```sh
CGO_ENABLED=1 go run .                  # run
CGO_ENABLED=1 go build -o dgopher .     # a single binary
CGO_ENABLED=1 go tool mygo build        # app bundle, installers and archives in build/ (mygo.json)
go run ./tools/icon                     # redraw resources/icon.png
go run ./tools/licenses                 # rewrite resources/THIRD_PARTY_NOTICES.txt
```

Run `go run ./tools/licenses` after changing `go.mod`: every bundle and
installer ships `LICENSE` and `resources/THIRD_PARTY_NOTICES.txt`, the
license files of every module linked in on any platform DGopher ships
for, and of the code DuckDB compiles in, kept in `tools/licenses/duckdb.txt`.
When `duckdb-go-bindings` moves to a new DuckDB release, the tool stops until
that file is rewritten from the release's source:
`go run ./tools/licenses -duckdb <DuckDB source directory>`.

With Nix, `nix develop` gives a shell with Go, the GTK libraries the
window needs and `CGO_ENABLED=1`. Elsewhere on Linux the window needs GTK
3 and OpenGL; no WebKitGTK is needed.

- **MyGo:** `go.mod` replaces MyGo with the `dbgopher` branch of
  [saanuregh/mygo](https://github.com/saanuregh/mygo/commits/dbgopher): upstream plus table
  header menus, frozen columns and multi-column sort, which the data view needs, and a paste fix
  (egoist/mygo#153). To update it, rebase that branch onto a new MyGo release and run
  `go mod edit -replace=github.com/egoist/mygo=github.com/saanuregh/mygo@<commit> && go mod tidy`.
- **Other platforms:** with cgo, building for macOS or Windows needs a C
  toolchain for that platform, or a machine of that system. For Windows,
  `zig cc` is enough to vet
  (`GOOS=windows GOARCH=amd64 CC="zig cc -target x86_64-windows-gnu" go vet ./...`)
  but not to link DuckDB, whose Windows library wants MinGW's C++ library:
  build on Windows.

## Releases

`.github/workflows/release.yml` builds and publishes a release when a tag
`v<version>` is pushed, the version of `mygo.json`, which it checks: each
platform builds on a runner of its own system, as DuckDB needs cgo, and
uploads its installers (the macOS disk image, the Windows installer, and
the Linux Debian package and archive) to the release, published once every
platform's are there, with a `SHA256SUMS` of them. `.github/workflows/ci.yml`
runs gofmt, vet and the tests on every push and pull request, on Linux and
Windows; a failure on Windows does not fail the run until the tests have
passed there once.

```sh
# set "version" in mygo.json, commit, then
git tag v0.2.0 && git push origin main v0.2.0
```

## Tests

```sh
go test ./...                                    # unit tests and UI tests without a window
DGOPHER_IT=1 go test ./...                      # also against real servers (below)
DGOPHER_SNAPSHOTS=/tmp/snaps go test ./...      # write PNGs of the UI as the tests see it
```

The README's and the docs' screenshots are drawn by `TestScreenshots`,
from the sample shop and connections made up around it, in the dark
theme. After a change to what they show, draw them again; text needs
Pango's library on the loader's path:

```sh
DGOPHER_SCREENSHOTS=$PWD/docs/images go test ./internal/app -run TestScreenshots
```

The integration tests expect these servers, whose ports and passwords
match `internal/db/integration_test.go` and `internal/testutil/testutil.go`.
SQLite and DuckDB need no server.

```sh
docker run -d --name dgopher-pg -p 127.0.0.1:15432:5432 -e POSTGRES_PASSWORD=dgopher postgres:17
docker run -d --name dgopher-mysql -p 127.0.0.1:13306:3306 -e MYSQL_ROOT_PASSWORD=dgopher -e MYSQL_DATABASE=shop mysql:8.4
docker run -d --name dgopher-ch -p 127.0.0.1:19000:9000 -p 127.0.0.1:18123:8123 -e CLICKHOUSE_USER=default -e CLICKHOUSE_PASSWORD=dgopher -e CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=1 clickhouse/clickhouse-server:26.3
docker run -d --name dgopher-redis -p 127.0.0.1:16379:6379 redis:7
# Redis 8, with its modules: JSON, Search, TimeSeries and vector sets, and
# its arrays.
docker run -d --name dgopher-redis8 -p 127.0.0.1:16385:6379 redis:8
# A cluster of three masters, and a master watched by a sentinel. They use the
# host's network, so that the addresses the nodes announce are reachable.
docker run -d --name dgopher-redis-cluster --network host redis:7 sh -c 'for p in 17000 17001 17002; do redis-server --port $p --bind 127.0.0.1 --cluster-enabled yes --cluster-config-file nodes-$p.conf --requirepass dgopher --masterauth dgopher --daemonize yes; done; sleep 1; redis-cli -a dgopher --no-auth-warning --cluster create 127.0.0.1:17000 127.0.0.1:17001 127.0.0.1:17002 --cluster-yes; exec tail -f /dev/null'
docker run -d --name dgopher-redis-sentinel --network host redis:7 sh -c 'redis-server --port 16380 --bind 127.0.0.1 --requirepass dgopher --daemonize yes; printf "port 26379\nbind 127.0.0.1\nrequirepass sentinelpw\nsentinel monitor mymaster 127.0.0.1 16380 1\nsentinel auth-pass mymaster dgopher\n" > /tmp/sentinel.conf; exec redis-sentinel /tmp/sentinel.conf'
```

Set `DGOPHER_CONFIG_DIR` to keep a development run's settings, and its
windows' layout, apart from your own; `DGOPHER_DATA_DIR` moves the layout
alone ([Where things are kept](storage.md)).

## Layout

| Path | What |
| --- | --- |
| `internal/db` | connections, sessions, cursors, the dialect of each engine, generated edits, and the Redis client |
| `internal/sqltext` | the SQL lexer: highlighting, splitting statements, classification for the safety policy, completion context, the formatter ([How SQL is read](sql.md)) |
| `internal/export` | CSV, TSV, JSON, JSON Lines, SQL, Markdown and Excel writers, and Parquet and DuckDB files through DuckDB. Text formats stay in Go, the writers of clipboard copies too, so that a copy and a file of the same rows agree. |
| `internal/datamodel` | data models: a schema's tables kept in a project's file, written as DDL for any engine, compared, and the migration between two |
| `internal/schemadoc` | the script creating a schema's objects, in an order that runs, and its documentation as HTML or Markdown |
| `internal/fileimport` | reads files to import: CSV, JSON and Parquet through DuckDB, Excel and XML loaded into it |
| `internal/decode` | turns values stored compressed or serialized into text: gzip, zlib, zstd, LZ4, Snappy, Brotli, MessagePack, pickle, PHP, Java, Protobuf |
| `internal/testdata` | makes up the values of generated rows, and suggests each column's generator by its type and name |
| `internal/sqlfile` | reads the statements of a SQL file as it streams, as a dump, `COPY` rows included |
| `internal/store` | private JSON files, keychain secrets, query history |
| `internal/secretcmd` | runs a password command without a shell |
| `internal/netproxy` | SOCKS5 and HTTP CONNECT proxies, and a local port forwarded through one; `proxytest` runs both for tests |
| `internal/sshtunnel` | SSH port forwarding, and dialing through SSH, with host key checks; `sshtest` is an SSH server for tests |
| `internal/audit` | the hash-chained audit log: append, read, verify |
| `internal/settings` | the user's settings and how values are shown |
| `internal/safety` | the safety policy: which statements are blocked or need a confirmation |
| `internal/redact` | hides secrets in text that is shown or logged |
| `internal/params` | query parameters: finding, typing and binding them |
| `internal/project` | project folders and their shared connections and snippets |
| `internal/connection` | a connection's live state: its pool, sessions, status and the schema read so far |
| `internal/ui/widgets` | the theme, icons, small controls, the confirm dialog and the tab contract |
| `internal/ui/editor` | the SQL editor |
| `internal/ui/dataview` | the result grid and its panels, table tabs, export, charts and the ER diagram |
| `internal/ui/query` | SQL editor tabs: running statements, scripts, plans and parameters |
| `internal/ui/dashboard` | dashboard tabs: panels of reads as charts, rows or values, their parameters and refresh, kept in a project's file |
| `internal/ui/modelview` | data model tabs: a model's tables, its DDL, and its comparison with a database or another model |
| `internal/ui/redis` | Redis tabs: the key tree, values and their editing, and the command console |
| `internal/app` | the window: navigator, tabs, menus, palette, dialogs and the quit flow |
| `internal/testutil` | shared test helpers: screenshots, waiting, the integration servers |
| `tools/icon` | draws `resources/icon.png` |
| `tools/licenses` | writes `resources/THIRD_PARTY_NOTICES.txt` |
| `main.go` | starts the app |

The tab packages (`dataview`, `query`, `redis`) each declare a small `Host`
interface for what they need from the window; `internal/app` implements
all of them.
