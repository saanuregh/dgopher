# Features

## Getting started

- **First run:**
  - Paste a connection URL (`postgres://…`, `mysql://…`, `clickhouse://…`,
    `redis://…`, `rediss://…`, or a file path) to fill the connection form.
  - Try the built-in sample shop database without a server.
- **Drag and drop:** drop on the window a SQLite or DuckDB file (opens as
  a connection), a `.sql` script (opens in an editor), a CSV, Parquet or
  JSON file (queried in place by DuckDB), or an Excel or XML file
  (imported into a new table).
- **Keyboard first:**
  - Every command is in the palette (⌘K), and tables open by name (⌘P).
  - ⌘1…⌘9 switch tabs (in the results grid, ⌘2 sorts by the chosen
    column instead); ⌘0 focuses the navigator; ⌘L focuses the current
    view's filter; ⌘J moves between the editor and its results; F5 runs or
    refreshes.
  - ⌘T opens an editor, ⌘N a connection, ⌘W closes a tab, Ctrl+Tab moves
    between tabs, and ⌘B shows or hides the sidebar.
  - ⌘/ lists every shortcut.
- **Window:** closing the last tab leaves the start page. On macOS,
  closing the window keeps DGopher running with its tabs and connections,
  and its Dock icon shows the window again.
- **Settings:** theme (System, Light or Dark), editor font size, rows per
  page, where statements end, what a script does on an error, and when
  to notify.
- **Notifications:** a statement, script, export or import that took 10
  seconds or more (a setting; 0 for never) tells the system as it ends,
  if DGopher is in the background then: what ran, on which connection,
  and its first error, never its SQL. A click brings its tab forward.

## Projects

- **Git-friendly projects:** every connection belongs to a project, a
  folder such as a repository. The sidebar lists every project with its
  connections and queries.
  - Create one with **New Project…**, or list an existing folder with
    **Add Existing Folder…** (⌘⇧O) or `dgopher <folder>`.
  - Its `dgopher.json` shares connections and snippets with the team,
    environments included. It holds no secrets.
  - The file is written sorted and stable, so it diffs cleanly. A file
    changed on disk, as by a `git pull`, is never overwritten: reload the
    project from its menu.
  - Every SQL editor is a file in `queries/`, saved as you type. A
    `-- connection: <id>` line binds a file to a connection, whose IDs
    complete as you type the line. A file changed on disk while open is
    never overwritten; the editor offers to use the file or keep yours. An
    editor closed untouched removes its file.
  - A file runs only on the connection its line names. An editor whose
    file names another connection runs nothing, and offers to switch to
    it; a file naming a connection the project lacks opens, unconnected,
    for its line to be fixed. A restored editor follows its file's line,
    as after a pull that changed it.
  - SQLite and DuckDB files inside the project are stored by relative path,
    so they open in every clone.

## Connections and navigator

- **Connections:** PostgreSQL (with its other databases), MySQL, ClickHouse
  (native protocol, or HTTP on 8123 and 8443), SQLite and DuckDB files, and Redis:
  one server, a cluster, or the master Sentinel names.
  - A Redis cluster starts from any of its nodes; the key browser scans
    every master, and the console reaches a key on whichever node holds
    it. Through Sentinel, the connection follows the master to its
    replacement after a failover, and logs in to the sentinels with
    credentials of their own. Through an SSH tunnel, every node is reached
    through the SSH server.
  - TLS: off, prefer, require, or verify with a custom CA. Prefer first
    checks for TLS with a handshake that sends no password; local
    connections skip TLS.
  - SSH tunnels: agent, key file with passphrase, or password. Host keys are
    checked against `known_hosts`, and a new host must be trusted explicitly
    after you compare its fingerprint. A changed host key is refused as a
    possible attack.
  - The connection form puts what most connections need on its General
    page: where the database is, who connects, the environment, a colour
    and read-only. Options (commit mode, timeouts, auto-connect) and
    Network (TLS, SSH) have pages of their own, marked when they hold a
    choice other than the default.
  - A connection's colour replaces its environment's in its tabs, the band
    above them and the status bar, which still names the environment.
  - New… makes an empty SQLite or DuckDB file; a file already there is
    opened as it is, never replaced.
  - Test Connection reports the server version and how long connecting took.
  - Each connection has an environment, and can be read-only, with its own
    commit mode and idle-transaction limit: see [Safety model](safety.md).
- **Navigator:** a lazy tree of databases, schemas, tables, views and columns,
  with row estimates. It has a filter, context menus, and quick open (⌘P).
  - Tables: view DDL, copy the name, export data, truncate or drop.
  - Connections: duplicate or delete; on Redis, open the key browser.
  - Projects: rename, show in files, or remove from the sidebar.

## SQL editor

- **SQL editor**, run as DBeaver runs it:
  - Syntax highlighting per dialect, and line numbers.
  - Completion of tables, columns (alias-aware), schemas and keywords.
  - A statement ends at `;` or at a blank line, so ⌘↵ runs the statement
    around the caret without selecting it; a band shows which. A setting
    keeps `;` only. A procedure, function or trigger body (`BEGIN … END`,
    `BEGIN ATOMIC`) stays one statement, and MySQL `DELIMITER` lines work
    as in the `mysql` client.
  - Run the statement (⌘↵), the selection, the whole script (⌘⇧↵ or Alt+X),
    or into a new result tab that keeps the others (`⌘\`). Result tabs can
    be pinned.
  - A script stops at its first error, or goes on, as a setting says.
  - `:name` parameters (also `@name` and `$name` on SQLite), ClickHouse
    `{name:Type}` parameters and `${var}` variables ask for their values
    first; all but `${var}` are sent apart from the SQL. A statement that
    mixes `:name` with `$1` or `?` placeholders is refused.
  - A failed statement shows the database's message, code, detail, hint and
    where it went wrong; "Go to Error" puts the caret there.
  - A right-click menu: execute, cut/copy/paste, format, upper or lower case,
    toggle comments, copy the statement, save a snippet, export from the
    query.
  - Explain (⌘E), format (⌘⇧F, the selection only when there is one,
    keeping the blank lines between statements), find (⌘F) and replace
    (⌘⌥F): one match at a time, or all at once, which a toast can undo.
  - An optional statement timeout per connection.
  - Each editor has its own session, so `SET`, temporary tables and
    transactions persist between statements.
  - Editors are restored on the next launch, and `.sql` files can be opened
    and saved. A restored editor does not connect on its own: Connect, or
    running a statement, opens its connection.
  - Auto-connect, off by default per connection: the connection opens as
    DGopher starts, and its editors connect as they are shown.

## Results grid and table tabs

- **Results grid**, one viewer for a query's results and a table's data,
  as DBeaver's:
  - Virtualized: only the rows in view are built.
  - Columns resize, move, sort by several at once (⌘-click), pin to the
    left, hide (or hide those with no data), and fit to their values or
    the screen, from the header's menu.
  - Filter by a cell's value (=, ≠, >, <, contains, NULL, the rows chosen),
    a typed value, the clipboard's value, or a list of distinct values with
    counts (⌘F11). The `WHERE` filter is SQL run by the server, with the
    recent filters and column names offered as you type: on a table, as
    its `WHERE`; on a query's result, around the statement
    (`SELECT * FROM (…) q WHERE …`), with its parameter values. The quick
    filter narrows the rows already read without running anything.
  - Sorting by a column runs on the server while rows remain to be read,
    and in memory once every row is, as DBeaver's default.
  - Refresh, Count (`SELECT COUNT(*)`), and Fetch All in both; a
    statement that writes is never filtered or counted, and refreshing its
    result asks before running it again.
  - A query result counts, lists distinct values and groups on the
    editor's own connection, so it sees the editor's schema, open
    transaction and temporary tables; while a result still reads its
    rows, Count waits for Fetch All.
  - Grid, record (Tab) and plain-text views (``⌘` ``).
  - Panels (F7): the value (text, JSON, XML, hex, image; save to or load
    from a file), Calc (count, distinct, sum, average, min, max, median),
    Profile, metadata, grouping, and the rows of other tables that refer
    to the chosen one.
  - Profile shows every column at once: its NULLs, distinct values,
    bounds, average and median, with a histogram of numbers and times, or
    the most frequent values of text. It profiles the rows read at once,
    and every row on the server when asked: DuckDB summarizes a table
    itself (SUMMARIZE); the other engines count in one statement.
  - Formats: thousands, dates, booleans, binary, NULL; row colours by
    value; zoom.
  - Copy, Advanced Copy (⌘⇧C), paste as pending changes (⌘V), and
    Advanced Paste (⌘⇧V). Copy as CSV, TSV, JSON, SQL, Markdown or a text
    table. Open the rows in a spreadsheet app.
  - Generate SQL for the chosen rows (SELECT, INSERT, UPDATE, DELETE, with
    their values), or for the table (with an upsert and joins, and `:name`
    parameters for the values).
  - Export to CSV, TSV, JSON, JSON Lines, SQL, Markdown, an Excel
    workbook, Parquet or a DuckDB database: the rows read, or the query run
    again (only a read runs again), up to a row limit that is kept from
    one export to the next (a million rows at first). A query result's
    export runs on the editor's session, so it sees the editor's schema,
    temporary tables and transaction, as the rows shown did; a table's runs
    on a session of its own. Reaching the limit is said, never silent.
    Files are named from a pattern (`${table}`, `${connection}`,
    `${timestamp}`, `${date}`); text formats can go to the clipboard
    instead. CSV can start with a byte order mark, for Excel.
  - The Excel workbook keeps numbers, dates and booleans as such, with
    a bold header that stays in view. A number past the 15 digits Excel
    keeps, such as a large ID, stays text, so that it is never rounded;
    rows, columns or text past Excel's limits stop the export rather than
    write a file Excel would repair.
  - Inline editing: change cells, add, duplicate and delete rows, set NULL
    or DEFAULT, revert, undo and redo (⌘Z), with DBeaver's keys.
  - A query's result is editable when its statement reads one table (no
    join, grouping, aggregate, `DISTINCT`, `UNION`, window function or
    subquery in `FROM`) and its columns include the table's key; computed
    and renamed columns stay read-only. The status line says why a result
    cannot be edited. Its edits apply on the editor's session, so with
    manual commit they join its open transaction, and ⌘S reviews them,
    then saves the file.
  - A table without a primary key becomes editable with a virtual key,
    kept in `dgopher.json` for the team, in its tab and in query results.
- **Table tab:**
  - Data ordered by primary key by default, with paging.
  - Structure: columns, indexes and foreign keys, with jumps to the
    referenced table.
  - The table's DDL, and a diagram of the tables that refer to it (on its
    left) and that it refers to (on its right).

## Redis

- **Redis:**
  - A SCAN-based key tree grouped by `:`, with pattern and type filters.
  - Viewers and editors for strings (JSON formatting), hashes, lists, sets
    and sorted sets; streams are view-only.
  - TTL, rename, delete and new keys.
  - A console with history (↑ ↓), which replies the way `redis-cli` does.
    `KEYS` is refused in favour of the SCAN-based tree.

## Charts, diagrams and server activity

- **Charts** of any result: bar, line, scatter or pie, with the axes
  detected (a time or text column along x, numbers as series), a hover
  tooltip, and a colour-blind-safe palette.
- **Server activity:** sessions and running queries of PostgreSQL, MySQL
  and ClickHouse, with cancel (and, on PostgreSQL and MySQL, terminate),
  and Redis's headline metrics and clients, which can be disconnected (of
  one node, in a cluster). It refreshes every 2 seconds.
- **ER diagrams** of a schema: tables with their columns and keys, and
  foreign-key connectors, laid out so referenced tables sit to the left.
  Drag tables to arrange them: one dropped on another moves to the
  nearest free place, so that no table hides another. Double-click one
  to open its data.

## Import, snippets and history

- **Import** a CSV, JSON, JSON Lines, Parquet, Excel or XML file into a
  table, or into a new table made from the file's columns (a schema's
  menu: Import File as New Table…, or drop an Excel or XML file):
  - DuckDB reads the file, finding a CSV file's delimiter and header, and
    every format's column types; the delimiter, the header and the sheet
    can be changed, every column read as text, and an XML file's rows
    chosen by their element. Nested JSON values become JSON text, and
    exact numbers keep their digits.
  - Into a table there, the file's columns map to the table's by name.
    Into a new table, each kept column takes a name and a type of the
    engine, and the CREATE TABLE shows before it runs.
  - It runs in one transaction, in INSERTs of many rows: a failing row
    imports nothing, and a table made for the import is not left behind.
    It waits for an open transaction on the database to end first.
  - On ClickHouse, which has no transactions, the rows before a failing
    one stay in a table that was there, and the error says how many.
- **Run SQL File…** runs a file of SQL, as a dump of `pg_dump`,
  `mysqldump` or `sqlite3`, without opening it in an editor, however large:
  - It reads the file through first, and says what it holds: how many
    statements of each kind, and each that destroys data, by line. The
    safety policy asks once for them all.
  - Then it runs the file as it reads it again, on a session of its own:
    in one transaction, all or nothing, where the database can hold one
    and the file holds none of its own; else statement by statement,
    stopping at the first error, or going on, as chosen. A file changed
    in between is not run.
  - A PostgreSQL dump's `COPY … FROM stdin` rows are loaded, and its
    `\restrict` lines skipped; other psql commands, as `\connect`, stop
    it, as only psql runs them. MySQL's `DELIMITER` and executable
    comments work as in the `mysql` client.
- **Snippets:** keep the selected SQL under a name, and insert it into any
  editor from the command palette.
- **History:** every statement and command is recorded, per project, with
  its timing and error (⌘Y). Open any entry again (↵) in a new editor of
  its connection.
