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

You need Go, a C compiler (cgo builds DuckDB and agent-cli-connector into
the binary), and on Linux GTK 3. The integration tests also need Podman
or Docker with compose, and PostgreSQL's client tools at the major
version of `compose.yaml`'s `postgres` image. Install them for your OS
below, then, from the repository's root:

```sh
go run ./tools/dev setup
```

It downloads the modules, registers the pre-commit check, and runs
`go run ./tools/dev doctor`, which lists what is still missing and how to
install it. Any Go from 1.21 on will do: it downloads the version in
`go.mod` by itself.

### Linux with Nix

`nix develop` gives a shell with Go, the GUI libraries the window loads,
`pg_dump`, podman-compose, docker-compose and `CGO_ENABLED=1` (see
`flake.nix`). Install Podman or Docker yourself; on NixOS,
`virtualisation.podman.enable = true;`.

### Debian and Ubuntu

```sh
sudo apt install golang build-essential libgtk-3-0 libsecret-1-0 libgl1 libxtst6 podman pipx
pipx install podman-compose==1.6.0
```

The distributions' `podman-compose` is too old to wait for the servers.
`pg_dump` 19 comes from PostgreSQL's own repository:

```sh
sudo apt install postgresql-common
sudo /usr/share/postgresql-common/pgdg/apt.postgresql.org.sh -y
sudo apt install postgresql-client-19
```

### Fedora

```sh
sudo dnf install golang gcc gcc-c++ gtk3 libsecret libglvnd-glx libXtst podman podman-compose
```

For `pg_dump` 19, add PostgreSQL's repository as
[its download page](https://www.postgresql.org/download/linux/redhat/)
shows, and install `postgresql19`.

### Arch

```sh
sudo pacman -S --needed go base-devel gtk3 libsecret libglvnd libxtst podman podman-compose
```

For `pg_dump`, install `postgresql-libs` when its major matches
`compose.yaml`'s `postgres` image; `doctor` says when it does not.

### macOS

```sh
xcode-select --install
brew install go podman podman-compose postgresql@19
podman machine init && podman machine start
```

`postgresql@19` is keg-only: put `$(brew --prefix postgresql@19)/bin` on
your `PATH`. On Apple silicon, `nix develop` also works.

### Windows

```powershell
winget install -e --id GoLang.Go
winget install -e --id Git.Git
winget install -e --id MSYS2.MSYS2
winget install -e --id RedHat.Podman
winget install -e --id Docker.DockerCompose
```

Then, in the MSYS2 UCRT64 shell, install the C compiler DuckDB's driver
needs, and add `C:\msys64\ucrt64\bin` to your `PATH`:

```sh
pacman -S --needed mingw-w64-ucrt-x86_64-gcc
```

Start Podman's machine with `podman machine init` and
`podman machine start`. Install PostgreSQL's command line tools from
[its Windows download page](https://www.postgresql.org/download/windows/)
for `pg_dump`. Docker Desktop works in place of Podman.

### Podman or Docker

`tools/dev` and the tests use Podman when it is installed, otherwise
Docker. Set `DGOPHER_CONTAINER` to `podman` or `docker` to choose.
`podman compose` runs `docker-compose` when it is installed, otherwise
`podman-compose`.

On Linux, Podman also needs its image policy (`policy.json`) and
subordinate IDs for your user; the distribution's package sets both up,
and on NixOS `virtualisation.podman.enable = true;` does. Through
`docker-compose` it needs its API socket too:
`systemctl --user enable --now podman.socket`, or set
`PODMAN_COMPOSE_PROVIDER=podman-compose`. `doctor` checks all three.

### The pre-commit check

`setup` registers `tools/githooks/pre-commit`, which runs gofmt (with
`go.mod`'s Go) and `go vet` before each commit, as CI does. It needs a Git
with config-based hooks (`git help hook`).

## Build and run

```sh
go run ./tools/dev run                  # run the app
go run ./tools/dev run --scratch        # with settings in a new temporary folder
CGO_ENABLED=1 go build -o dgopher .     # build a single binary
CGO_ENABLED=1 go tool mygo build        # app bundle, installers and archives in build/ (configured by mygo.json)
```

`tools/dev` sets `CGO_ENABLED=1` itself, so its commands work the same in
any shell. Elsewhere, set it as your shell does; in PowerShell,
`$env:CGO_ENABLED = "1"`.

`--scratch` sets `DGOPHER_CONFIG_DIR` to keep a development run's
settings and window layout apart from your own. `DGOPHER_DATA_DIR` moves
only the layout. See [Where things are kept](storage.md).

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
go run ./tools/dev check                     # gofmt, go vet and go test ./..., as CI runs them
go run ./tools/dev test                      # unit tests and headless UI tests
go run ./tools/dev test -- ./internal/sqltext -run TestSplit    # one test
go run ./tools/dev servers up                # start the integration servers (below)
go run ./tools/dev test --it                 # also against those servers
go run ./tools/dev test --snapshots          # write PNGs of the UI as the tests see it
```

Run `check` before calling a change done. Run the integration tests too
when the change touches `internal/db`.

[Testing](testing.md) explains how to write each kind of test, and which
run on each OS.

### Integration servers

Integration tests run only with `DGOPHER_IT=1`, which `test --it` sets.
They call `testutil.Integration(t)`, which serializes them across
packages on Unix; elsewhere `test --it` adds `-p 1`. SQLite and DuckDB
need no server.

[`compose.yaml`](../compose.yaml) runs every server the tests use:
PostgreSQL, MySQL, ClickHouse, Redis 7 and 8, a Redis cluster, and a Redis
master with a sentinel. Their ports and passwords match
`internal/testutil/testutil.go`, `internal/db/integration_test.go` and,
for Redis 8, `internal/db/redismodules_test.go` and
`internal/ui/redis/redis_test.go`.

```sh
go run ./tools/dev servers up        # start them with Podman or Docker, and wait until they are ready
go run ./tools/dev test --it         # run every test against them
go run ./tools/dev servers down      # stop and remove them
```

Servers of the engines built on another's (MariaDB, TiDB, CockroachDB,
YugabyteDB and TimescaleDB), and of SQL Server and Oracle, start only with
their profile, and their tests skip, naming the command, when they are not
running. Their addresses are in
`internal/testutil/optionalserver/optionalserver.go`:

```sh
go run ./tools/dev servers up --profile compat       # also MariaDB, TiDB, CockroachDB, YugabyteDB and TimescaleDB
go run ./tools/dev servers up --profile enterprise   # also SQL Server (x86-64 only, about 2 GB) and Oracle Free
go run ./tools/dev servers up --profile enterprise oracle   # only Oracle
```

Oracle Free (service `oracle`, container `dgopher-oracle`) uses up to
2 GB of memory, its edition's cap, and an image of several GB. Once the
image is pulled it is ready in about ten seconds. Tests connect to its
`FREEPDB1` service as `dgopher`, or as `SYSTEM` for what needs a DBA;
backup tests run stand-ins for `expdp` and `impdp`. Two listeners also
speak TCPS: one on a port of its own, and one on another port that asks
for a client certificate (`compose.yaml`; both `oracle-tcps` entries in
`optionalserver.go`), set up as it starts by
`internal/db/testdata/oracle-tls/tcps.sh` with the wallet beside it. That
wallet and the CA signing the tests' client certificates are written by
`TestWriteOracleTLS` in `internal/db/oraclewallet_test.go`; after writing
them again, make the container again with `podman compose --profile
enterprise up -d --force-recreate --wait oracle` (or `docker compose`). A
container made before it had TCPS gets it from `go run ./tools/dev servers
up --profile enterprise oracle`, which makes it again.

The Redis cluster and sentinel use the host's network, so the addresses
the nodes announce are reachable from the tests. This needs Podman or
Docker on Linux, or Docker Desktop with host networking turned on. With
Podman's machine on macOS or Windows, the host is the machine's Linux VM,
so their tests cannot reach them.

### Screenshots

`TestScreenshots` draws the screenshots in the README and these docs, in
the dark theme (and the main window once in light), from the sample shop
and connections made up around it.
Draw them again after a change to what they show. Text needs Pango's
library on the loader's path, which `nix develop` provides.

The query plan, Server Activity and Redis images need the integration
servers (`go run ./tools/dev servers up`), so they come only with
`DGOPHER_IT`. Without it, `TestScreenshotsServers` skips and leaves them as
they are.

```sh
DGOPHER_IT=1 DGOPHER_SCREENSHOTS=$PWD/docs/images go test ./internal/app -run TestScreenshots
```

## Release

Pushing a tag `v<version>` builds and publishes a release
(`.github/workflows/release.yml`):

1. Set `"version"` in `mygo.json`, add a `## <version>` section to
   `CHANGELOG.md`, and commit. The workflow checks that the tag matches the
   version. The update window shows that section as the release notes, and
   a signed build fails without it.
2. Tag and push:

   ```sh
   git tag v0.2.0 && git push origin main v0.2.0
   ```

Each platform builds on a runner of its own system, because DuckDB needs
cgo. `go tool mygo build -upload` uploads its installers, the macOS disk
image, the Windows installer, the Linux Debian and RPM packages and archive, and
the install scripts, `install.sh` for macOS and Linux and `install.ps1`
for Windows, with its signed update: an archive of the app, a manifest
(`update-<platform>.json`) and deltas from earlier versions. The release
is published, with a `SHA256SUMS` of every file, once every platform's
files are there. Installed apps then update to it.

### The update signing key

Updates are signed with the secret key of `go tool mygo keygen`, and the
app accepts only updates signed with it. The public key is
`updates.publicKey` in `mygo.json`. The secret key is the repository's
`MYGO_UPDATER_PRIVATE_KEY` secret, and the release fails without it:

```sh
gh secret set MYGO_UPDATER_PRIVATE_KEY < ~/.config/mygo/update-keys/mygo-update.key
```

Keep a backup of `mygo-update.key` outside this machine, such as in a
password manager. Losing it strands installed apps: they never accept an
update again, and users must reinstall. Never commit it.

A local build signs its update when given the key:

```sh
MYGO_UPDATER_PRIVATE_KEY="$(cat ~/.config/mygo/update-keys/mygo-update.key)" CGO_ENABLED=1 go tool mygo build
```

The macOS app, the Windows installer's per-user install, and the Linux app
of `install.sh` or of the extracted `.tar.gz` update themselves. The
`.deb` and `.rpm` install into `/opt`, which the app cannot write, so they
do not.

`.github/workflows/ci.yml` runs `go run ./tools/dev check` on every push
and pull request, on Linux, macOS and Windows. It does not run the
integration tests.

## MyGo

`go.mod` replaces MyGo with a commit of a `dbgopher-v<version>` branch of
[saanuregh/mygo](https://github.com/saanuregh/mygo/branches/all?query=dbgopher).
The branch is named for the MyGo release it is based on, with `-2`, `-3`
and so on when it is rebuilt on the same release. A branch on upstream's
`main` between releases, for a change no release has yet, is named
`dbgopher-main-<date>`; `go.mod` then still requires the last release.

The branch is upstream MyGo plus the changes DGopher needs that upstream
does not have yet:

- the commits of its open pull requests to
  [egoist/mygo](https://github.com/egoist/mygo/pulls?q=author%3Asaanuregh),
  as they were sent;
- frozen table columns;
- the code editor's `GutterButtons`, `OnAcceptCompletion` and
  `CompletionsOpen`;
- the code editor's `LinkAt` and `OnLink`, which show a name as a link
  while ⌘ is held and open it on a click; the name takes the place of the
  word upstream's `OnDefinition` link would show.

An API missing from upstream MyGo may exist there.

To move to a new MyGo release:

1. Create a new branch from the release and apply those commits, dropping
   the ones upstream has taken. Make a new branch rather than rewriting the
   old one, so the commit `go.mod` pins now stays reachable.
2. Require the release and point `go.mod` at the new commit:

   ```sh
   go mod edit -require=github.com/egoist/mygo@v<release> \
     -replace=github.com/egoist/mygo=github.com/saanuregh/mygo@<commit> && go mod tidy
   ```

## agent-cli-connector

The AI chat drives `claude`, `codex` and `pi` through
[agent-cli-connector](https://github.com/saanuregh/agent-cli-connector), a
Zig library. Its Go binding links the library into the binary with cgo, as
duckdb-go does with DuckDB: a module per platform,
`bindings/go/lib/<os>-<arch>`, holds the static library, so nothing is
loaded or downloaded at run time.

To work on both at once, build the static libraries in the checkout
(`zig build go-libs`), then point `go.mod` at it: the binding and each
platform's module. Before committing, drop the replaces and require the
published version, `vX.Y.Z` (the connector's release that holds the
change):

```sh
mod=github.com/saanuregh/agent-cli-connector/bindings/go
libs=$(cd ../agent-cli-connector/bindings/go/lib && ls -d */ | tr -d /)
go mod edit -replace=$mod=../agent-cli-connector/bindings/go
for p in $libs; do go mod edit -replace=$mod/lib/$p=../agent-cli-connector/bindings/go/lib/$p; done
go mod tidy

go mod edit -dropreplace=$mod
for p in $libs; do go mod edit -dropreplace=$mod/lib/$p; done
go get $mod@vX.Y.Z && go mod tidy
```

The chat's tests talk to the library's built-in `fake` provider
(`chat.FakeAgent`), which replies with the message it was sent. To try
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

Engine behavior lives in each engine's rules in `internal/db/engine.go`;
see [Architecture](architecture.md#engines).

### Data in and out

| Path | What |
| --- | --- |
| `internal/export` | CSV, TSV, JSON, JSON Lines, SQL, Markdown and Excel writers, and Parquet and DuckDB files through DuckDB. Text formats are written in Go, clipboard copies too, so a copy and a file of the same rows agree. |
| `internal/dbeaver` | reads the connections DBeaver Community Edition saved, and their passwords, as DGopher connections |
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
| `internal/containercli` | chooses Podman or Docker for the integration servers, for `tools/dev` and the tests |
| `internal/testutil` | shared test helpers: snapshots, waiting for frames, the integration servers, and the open-file check; `serverlock` serializes integration tests |
| `tools/icon` | draws `resources/icon.png` |
| `tools/licenses` | writes `resources/THIRD_PARTY_NOTICES.txt` |
| `tools/githooks` | the optional pre-commit check (see [The pre-commit check](#the-pre-commit-check)) |
| `tools/dev` | the contributor commands: `doctor`, `setup`, `check`, `test`, `servers` and `run` |
