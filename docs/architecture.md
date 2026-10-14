# Architecture

How DGopher's parts fit together. [Development](development.md#code-layout)
lists what each package holds; this page explains how they work together.
Function names are given so you can search for them; read the code for the
details.

- [The UI model](#the-ui-model)
- [Tabs](#tabs)
- [Running a statement](#running-a-statement)
- [Connections and sessions](#connections-and-sessions)
- [Engines](#engines)
- [Persistence](#persistence)
- [Secrets](#secrets)

## The UI model

DGopher's UI is drawn by [MyGo](https://mygo.egoist.dev) in immediate mode:
every frame redraws the window from plain Go state. No widget tree holds
state on its own.

- `app.Run` (`internal/app/run.go`) takes the single-instance lock, opens
  the config store and `ui.sqlite`, and creates the `App`.
- Each frame, `App.windowView` (`internal/app/window.go`) first runs the
  work posted since the last frame (`App.drain`), then draws (`App.view`).
  The frame also checks idle transactions and idle connections.
- State lives in structs: `App` (`internal/app/app.go`) holds the windows,
  connections, projects, dialogs and job list; each `window` holds its
  tabs; each tab owns its own state.

### Main thread and background work

All UI state, including every `connection.Conn`, is read and changed on
the main thread only. Work that blocks (a query, a connect, a file read)
runs in a goroutine and hands its result back:

- **`Post(fn)`** queues `fn` to run on the main thread before the next
  frame, and wakes the window.
- **`Background(work)`** runs `work` in a goroutine. `work` returns a
  function, which is then posted to the main thread. The busy indicator
  counts these.
- **`dataview.RecoverBackground`** is deferred in every goroutine. It turns
  a panic into a redacted log line and a "Something went wrong" message
  instead of a crash.
- **`dataview.RunJob`** runs long work that quitting or disconnecting must
  ask about (an import, a copy, a backup); see
  [Closing, disconnecting and quitting](safety.md#closing-disconnecting-and-quitting).

**Rule:** a goroutine never touches UI or connection state directly. It
returns a closure from `Background`, or calls `Post`.

## Tabs

### The tab contract

Every tab implements `widgets.Tab` (`internal/ui/widgets/tab.go`):
`Title`, `Connection`, `View`, `CloseReason` (what closing would lose) and
`Close`. Optional interfaces let a tab say more about itself: `Iconed`,
`Placed` (the connections and databases it runs on), `ConnectionHolder`
(keeps its connection from idling out), `ShownNotifier` (told when it comes
into view), `Marked`, `Worker` (busy) and `Commander` (its palette
commands).

### The tab list

`tablist.List` (`internal/ui/tablist`) keeps a window's tabs and their
order, pins, the tab in front and the tab beside it. It is changed only
through its methods, which keep its rules: pinned tabs come first, and the
tab beside is never the tab in front.

### Host interfaces

Each tab package declares a small `Host` interface for what it needs from
the window, so it does not import `internal/app`:

| Package | Host |
| --- | --- |
| `internal/ui/dataview` | `dataview.Host`: settings, toasts, the clipboard, connecting, opening tabs, confirmations, audit recording and jobs, plus `Post` and `Background` |
| `internal/ui/query` | `query.Host`: `dataview.Host`, plus dialogs, snippets, saving files and switching connections |
| `internal/ui/redis` | `redis.Host`: its own subset, with confirmations and audit recording |
| `internal/ui/dashboard` | `dashboard.Host`: finding a project's connections |
| `internal/ui/modelview` | `modelview.Host`: `dataview.Host`, plus finding connections |

`*app.App` implements all of them. Tests use `dataview.FakeHost` instead;
see [Testing](testing.md).

## Running a statement

What happens when you press ⌘↵ in an editor
(`internal/ui/query/query.go`):

1. **`Tab.Run`** refuses while a run or its results are busy. If the
   connection is not connected, it connects first and runs when that ends.
2. **`Tab.run`** picks the statements (at the caret, the selection or the
   whole script), adds the `EXPLAIN` prefix when explaining, and asks for
   missing parameter values.
3. **`Tab.runBound`** binds parameters, then asks the safety policy:
   - `safety.Analyze` classifies each statement
     ([How SQL is read](sql.md#classification)).
   - `safety.ReviewSQL` returns a verdict: blocked, needs confirmation
     (perhaps with the connection's name typed), and why.
   - A blocked statement is recorded (`RecordBlocked`), shown, and never
     sent.
   - A statement that needs confirmation goes through `App.AskConfirm`,
     which records the confirmation in the audit log when you agree.
4. **`Tab.execute`** runs in a goroutine. It opens the editor's session if
   needed, then for each statement:
   - in manual commit, begins a transaction before the first write;
   - for a read, calls `Session.Query` and fetches the first page from the
     `db.Cursor`; a plan goes through `db.ParsePlan` and `db.Advise`;
   - for a write, calls `Session.Exec`, wrapped in a transaction of its own
     when the [large-change check](safety.md#large-changes) applies.
5. **Recording.** `App.RecordTyped` redacts the entry and appends it to the
   audit log, and to the query history.

Separately, every statement any driver sends, including the app's own
catalog reads, is reported by a hook in `internal/db/wire.go`
(`db.SetOnSend`). `App.recordSent` truncates and redacts it and queues it
as [traffic](audit-log.md#traffic).

Other places that run SQL (the schema switcher, Run SQL File, grid edits,
schema changes) use the same `Analyze` and `ReviewSQL` pair. Redis
commands use `safety.ReviewRedis`.

## Connections and sessions

### Connecting

`connection.Conn` (`internal/connection`) is a connection's live state:
its config, status (idle, connecting, connected or failed), pool, server
version, and the schema read so far.

`App.Connect` runs the trust check for connections from a project file and
any password command, then opens the pool (`db.Open`, or `db.OpenRedis`) in
the background. `Conn.Generation` counts disconnects, so a connect that
finishes after you cancelled or disconnected is dropped.

### Pools and sessions

- `db.DB` (`internal/db/open.go`) wraps a `database/sql` pool. On
  PostgreSQL it keeps a pool per database.
- Each editor has its own `db.Session` (`internal/db/session.go`), pinned
  to one server connection, so `SET`, temporary tables and transactions
  last between statements. Closing a session rolls back what it left open.

### Lost connections

When the driver reports a connection broken, the session drops it. If a
transaction was open, the error says it was lost. The first statement
after the connection is remade gets `db.ErrSessionReset` instead of
running, which is the refusal described in
[After a lost connection](safety.md#after-a-lost-connection).

### Idle limits

`connection.IdleTxLimit` and `connection.IdleConnectionLimit` give each
connection's limits (its own, or the environment's default). Each frame,
`App.checkIdleTransactions` (`internal/app/txsafety.go`) warns and then
rolls back, and `App.checkIdleConnections` (`internal/app/idleconn.go`)
disconnects connections that hold nothing.

### The single shared connection

DuckDB, and SQLite in memory, use a pool of exactly one connection
(`DB.single`), because each separate connection would see a different
database. All sessions on it share one transaction:

- The `db.DB` records which session began the transaction
  (`Session.OwnsTx`).
- Only that session may commit or roll back. Others get `db.ErrNotOwner`,
  and `Begin` is refused while another session's transaction is open.
- The app follows the shared state each frame (`followSharedTx`), so every
  tab shows who owns the transaction.

## Engines

- `db.Engine` (`internal/db/config.go`) names an engine. The `engineInfos`
  table holds each engine's label, default port and user, version query,
  lexer dialect, `Dialect` and backup formats.
- `db.Dialect` (`internal/db/dialect.go`) is each SQL engine's behavior:
  quoting, placeholders, catalog queries, generated SQL. Each engine has its
  own file (`postgres.go`, `mysql.go`, …). Redis has no `Dialect`.
- **Engine behavior is not only in `Dialect`.** Switches on `db.Engine` are
  spread across `internal/app`, `internal/ui/*`, `internal/datamodel`,
  `internal/export` and `internal/safety`. Search for `case db.<Engine>`
  when changing an engine. [Engine support](engines.md) lists the
  differences users see.

## Persistence

| What | Where | Code |
| --- | --- | --- |
| Shared project files | `dgopher.json`, `queries/`, `dashboards/`, `models/` | `internal/project`, `internal/ui/dashboard`, `internal/datamodel` |
| Your project state and audit log | `.dgopher/state.sqlite` | `internal/state`, `internal/audit` |
| Window layout | `ui.sqlite` in the data directory | `internal/state` (`OpenUI`) |
| Settings, trust, known hosts | the config directory | `internal/store` |
| Passwords | the system keychain, service `DGopher` | `internal/store` (`KeyringSecrets`) |

[Where things are kept](storage.md) and [Project files](project-files.md)
describe the files.

## Secrets

- **Redaction.** `internal/redact` hides secrets in text that is shown or
  logged: `redact.Secrets` for statements, `redact.Error` for errors that
  quote them, and `redact.Redis` for Redis commands. Every audit entry,
  history entry, notification, panic log and Redis console line goes
  through it.
- **Password commands.** `secretcmd.Run` (`internal/secretcmd`) runs a
  command without a shell. Its output never appears in an error, and
  `secretcmd.AuditText` drops its stderr before the audit log.
- **Keychain.** `store.KeyringSecrets` keeps passwords under the service
  `DGopher`. Tests use `store.MemorySecrets`.
