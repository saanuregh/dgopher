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
  groups of tabs; each tab owns its own state.

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

### Groups and the tab list

A window's work area is a tree of splits, `panes.Layout`
(`internal/ui/panes`): an inner node splits two children side by side or
one above the other, by percent; a leaf is a `panes.Group`, whose
`tablist.List` (`internal/ui/tablist`) keeps the group's tabs, their order,
pins and the tab in front. Both change only through their methods: a split
always has two children, the last group never goes, and pinned tabs come
first.

One group is focused. `window.tabs()` is its list, so code acting on "the
tabs" acts on the focused group; code meaning every tab iterates
`window.allTabs()` or `App.everyTab()`. `App.KeysTo(t)` is true only for the
focused group's tab in front, which is how a tab knows that keys are its.
`App.removeTab` reports the group a tab left, and a group left empty is
taken away after the frame (`dropEmptyGroup`).

### Keys

`internal/keymap` holds every action with keys and its default keys, by a
Zed-style name such as `editor::Run`, and the user's overrides from
`keymap.json` (`keymap.Parse`, `keymap.Use`). A place an action works asks
`keymap.Pressed(c, id)` each frame. Keys of two in a row go through
`keymap.Dispatch`, called once a frame at the window: it claims each
sequence's first key, then the keys that can follow, and marks the action
fired for the frame. An action of the window gives way to the same key of
a narrower context that asked for its keys in the last frame
(`shadowed`), as Zed's narrower context wins. `App.watchKeymap` reads the
file again when it changes on disk.

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
| `internal/ui/chat` | `chat.Host`: `dataview.Host`, plus the connections; `chat.Editor`: the editor a chat is about |

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

### The notebook view

A SQL editor tab (`query.Tab`) shows as a script or as a notebook
(`internal/ui/query/notebook.go`). `Tab.Editor` is the editor the keys go
to: the script's, or the focused cell's, so completion, cards, find,
format and the run keys work in a cell unchanged. `Tab.script` holds the
file's text in both views; the notebook joins its cells into it
(`syncNotebook`) and reads them again when it changes otherwise, as on
disk.

Every cell runs on the tab's one session through the same `Run`. A run
first binds the cell (`bindCell`): its results become `Tab.results`, which
the run replaces and appends to, and the cell bound before keeps its own.
Checks that mean every result, such as pending changes, busy results or
open cursors, read `allResults`; a run cuts short the cursors other cells
still read, as the session runs one statement at a time. Run All advances
from the run's end (`cellRunEnded`), never from the view.

### AI chat

`chat.Chat` (`internal/ui/chat`) is a SQL editor's chat: `query.Tab`
holds one, drawn beside the editor, and is its `chat.Editor`, which gives
the editor's connection, what it shows (`EditorContext`) and `InsertSQL`.
`lockDown` starts the agent with DGopher's tools alone: Claude Code
without built-in tools (`--tools ""`), settings sources or MCP servers from
settings; pi without built-in tools, extensions, skills, prompt templates,
MCP or project trust; both in the `dontAsk` permission mode, which refuses
any other call without asking. Codex has no switch for its tools: its
features that give tools are disabled (`codexFeaturesOff`, less those the
installed Codex lacks, read once from `codex features list`, as it refuses
to start on a name it does not know), web search and
the config's MCP servers too (`codexConfig`), and it runs in the
`readOnly` mode, its read-only sandbox. Code mode stays, as its models
reach DGopher's tools through it, and with it `apply_patch`, which asks
first. Whatever an agent asks the user to allow, the chat refuses unasked
(`apply`): that refusal is what keeps Codex's patches out. `chart_in_editor` asks `chat.Editor.ChartSQL` to put a read in the
editor and run it through `query.Tab.Run`; the run's end (`runEnded`)
shows its first result's rows in the Chart view with the agent's
`dataview.ChartSettings` (`Viewer.ShowChart`) and tells the tool of the
rows. The run records `audit.TypedRun.Purpose`, which makes it the app's.
Each message begins with an `<editor>` block made from `EditorContext`,
which the view strips back off, showing its summary. The chat talks to an
agent's command line through
[agent-cli-connector](https://github.com/saanuregh/agent-cli-connector).
The session's events are read on a goroutine and posted, in order, to the
main thread, where `agent.Transcript` folds them into items to draw.
Requests that need the user, the agent's permissions and questions and the
app's tools' reads, wait in `Chat.requests` until answered. The models come
from the provider's `ListModels`, read once per agent as the model menu
opens or a session starts; the model and effort chosen go into the
session's `Config`, or to `SetModel` and `SetEffort` once it runs.

The app's tools (`tools.go`) run on the connector's goroutines: they read
app state through `onMain`, which posts and waits; catalog reads go to the
pool directly. `run_read_query` refuses what the safety policy does not
read as a read needing no confirmation, and production; then waits for the
user's answer on a channel, runs on a session of its own as a job of the
connection, and records the run in the audit log.

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
| Passwords | the system keychain, service `DGopher` | `internal/store` (`MygoSecrets`) |

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
- **Keychain.** `store.MygoSecrets` keeps passwords under the service
  `DGopher` with MyGo's `Secrets`. A password an older version kept with
  go-keyring (`store.KeyringSecrets`) moves there the first time it is
  read; the old item goes only once the new one reads back the same, and
  never on macOS, where both are one Keychain item. Where MyGo's store
  cannot work (Linux without libsecret), go-keyring keeps the passwords as
  before. Tests use `store.MemorySecrets`.
