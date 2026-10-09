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
  - Every command is in the palette (⌘K), and tables, functions and
    procedures open by name (⌘P).
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
  - TLS: off, prefer, require, or verify with a custom CA, and a client
    certificate to log in with where the server asks for one. Prefer
    first checks for TLS with a handshake that sends no password; local
    connections skip TLS.
  - Proxies: SOCKS5 or HTTP CONNECT, with a user and a password kept in
    the keychain, to the server or to the SSH host.
  - Cloud identities log in to PostgreSQL and MySQL without a password:
    a token of your AWS IAM, Google Cloud IAM or Microsoft Entra ID login,
    printed by the cloud's own tool (`aws`, `gcloud`, `az`) after its
    single sign-on, made again for new connections as it ages, and sent
    only over TLS. MySQL can send the password as clear text, over TLS,
    as LDAP and PAM logins need; PostgreSQL's LDAP and PAM logins take the
    password as any other.
  - Kerberos: a PostgreSQL server asking for it gets the ticket of your
    `kinit`, from the credentials cache of `KRB5CCNAME` as `krb5.conf`
    (or `KRB5_CONFIG`) sets up; on Windows, your login's credentials.
  - SSH tunnels: agent, key file with passphrase, or password, through
    jump hosts as `ssh -J` does. Host keys are checked against
    `known_hosts`, a jump host's too, and a new host must be trusted
    explicitly after you compare its fingerprint. A changed host key is
    refused as a possible attack.
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
  - A schema's other objects, each in a folder of its kind: functions and
    procedures, triggers, sequences, types and extensions (PostgreSQL),
    events (MySQL), macros (DuckDB) and projections (ClickHouse). One opens
    its definition in an editor, where it can be changed and run.
  - A PostgreSQL partitioned table lists its partitions with their bounds:
    open one's rows, detach it, or make a new one, its statement written
    for the table's strategy.
  - Tables: view DDL, copy the name, export data, truncate or drop.
  - The table form makes a table (New Table…, on a schema) or changes
    one (Edit Structure, on its Structure page): columns renamed, retyped,
    made nullable, defaulted, commented, added and dropped; the primary
    key; indexes, foreign keys with their actions, and checks, dropped or
    added. Review SQL shows every statement before it runs; they run in
    one transaction where the engine's DDL allows it, and the
    confirmation says when it does not. SQLite makes the table again for
    what ALTER TABLE cannot change, keeping its rows, indexes, triggers
    and views, and refuses when the table has what the form cannot write
    again, as a collation. DuckDB and ClickHouse change the columns of a
    table they made, not its keys.
  - Generate SQL Script and Generate Documentation, on a schema, write
    the objects chosen from it. The script creates them in an order that
    runs: types and functions first, then tables, each after those its
    foreign keys point at, views after what they read, and triggers last;
    it opens in an editor. The documentation, one HTML page or a Markdown
    file, lists each table's columns, keys, indexes and references, and
    each object's definition.
  - Search Objects finds the tables, views, columns, routines, triggers,
    sequences and types of every schema of a database by name, and with
    In definitions, views by their query and routines and triggers by
    their body, showing the line that matched. Enter opens what it found:
    a table's rows, a column's structure, or a definition.
  - Rename a table, a view or a column of a table, through the same
    safety review as a statement typed in an editor; SQLite cannot rename
    a view.
  - Connections: duplicate or delete; on Redis, open the key browser.
  - Projects: rename, show in files, or remove from the sidebar.

## SQL editor

- **SQL editor**, run as DBeaver runs it:
  - Syntax highlighting per dialect, and line numbers.
  - Catalog Queries, on a connection's menu or in the command palette,
    lists the latest queries the app ran on its own to read the catalog,
    as the navigator's tables and an editor's completions, with how long
    each took, its arguments and its error.
  - Two tabs side by side: Open to the Side, in a tab's menu, shows it
    beside the tab in front, with a divider to drag. The keys go to the
    one in front, marked above it; clicking into the other brings it in
    front where it is.
  - Mistakes are marked as you type, with a wavy line: a string, quoted
    name or comment left open, a parenthesis that closes nothing or is
    not closed, and, against the catalog read, a table or a column
    (`alias.column`) that does not exist. The line under the editor says
    what is wrong at the caret, with the closest existing name as a fix
    that ⌥↵ applies. Statements making or dropping tables are not
    checked for names.
  - Go to Definition (F12 or ⌘B) opens what the name at the caret names:
    a table's or a view's structure, through an alias or a column of it
    too, or a routine's definition. Find Usages (⇧F12) lists where the
    name is used in the file and the project's other query files; Rename
    in File (F2) renames it everywhere in the file, quoting it where it
    needs quotes, while the navigator's Rename renames it in the database.
  - Go to Statement (⌘⇧O) lists the file's statements, by line, with the
    comment above each, filtered as you type; Enter goes to one.
  - The editor's toolbar shows the schema it finds names in, and on
    PostgreSQL its database, and switches them: `SET search_path` or `USE`
    on its session, audited, and another database on a session of its
    own, never with a transaction open. A `SET` or `USE` typed moves it
    too.
  - Completion of tables, columns (alias-aware), schemas, keywords, the
    engine's common functions and the schema's own, which complete with
    their parentheses, and snippets by their keyword.
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
  - A plan (⌘E), or one measured by running the statement (Explain
    Analyze, ⌘⇧E, for statements that read, on PostgreSQL, MySQL and
    DuckDB), shows as a tree of its steps, with their rows, estimated and
    found, and their share of the time or cost; as a flame graph; or as
    the rows the server gave. Advice above it points at what may make the
    query faster: a scan reading every row to keep few, estimates far from
    what ran, a sort or hash spilling to disk, a nested loop rescanning a
    table, a ClickHouse key that narrows nothing. A typed `EXPLAIN` in
    those forms shows so too.
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
  - The filter builder, beside the `WHERE` box, writes that filter from
    conditions chosen: a column, an operator (comparisons, contains,
    starts or ends with, one of a list, between, NULL) and a value, all
    or any of them; the box then shows the SQL it wrote.
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
  - Export Tables, on a schema, writes the tables and views chosen, each
    to a file of its own named by the pattern, with the same formats and a
    limit for each.
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
  - Compare two results: pin a result's rows, then compare another with
    them, from the grid's compare button. The comparison matches rows by
    the table's key, a column of both or their position, and shows the
    rows that differ, A's above B's with the changed cells marked, and
    those only in one.
  - Edit Value… (⇧↵) edits a cell in the form its type suits, beside its
    text: a tree of a JSON value, the list of an enum's values (read from
    PostgreSQL's catalog, or spelled by the type elsewhere), a calendar
    and the time for dates, a switch for booleans.
  - A foreign key's cell takes its value from the table it points at:
    Choose from…, in the cell's menu, or ⌥↓, lists that table's rows by
    key with their first columns, searched by key or text.
  - A table without a primary key becomes editable with a virtual key,
    kept in `dgopher.json` for the team, in its tab and in query results.
  - Sensitive values stay off the screen: columns named as passwords,
    tokens, keys or card numbers show `••••••` in the grid, the value
    panel and the figures of the profile, calculation and grouping
    panels. A column's header menu hides or shows any column's values,
    for the team in `dgopher.json` when the rows are a table's; a setting
    shows the sensitive ones. Copies and exports keep the real values.
- **Table tab:**
  - Data ordered by primary key by default, with paging.
  - Structure: columns, indexes and foreign keys, with jumps to the
    referenced table.
  - The table's DDL, and a diagram of the tables that refer to it (on its
    left) and that it refers to (on its right).

## Redis

- **Redis:**
  - A SCAN-based key tree grouped into folders by `:`, or the separator
    the connection sets, with pattern and type filters.
  - Viewers and editors for strings (JSON formatting), hashes, lists, sets,
    sorted sets and streams: entries added and deleted, and consumer
    groups created and destroyed, with each group's consumers and the
    entries pending with them, acknowledged or claimed for another
    consumer. A list's item is removed at its own index, never another
    of the same value. A key's items are read 500 at
    a time, with more on request; a string's first 64 KB, and the rest
    on request: a string shown in part is not editable, as saving it
    would cut it.
  - Redis 8's types: a JSON document edited whole, formatted and checked
    as JSON before JSON.SET saves it; an array's values read in order of
    their indexes, set at an index, inserted at its next, and removed;
    a vector set's elements, read with their attributes, added with
    their vectors, and removed, the chosen one showing its vector, its
    attributes to edit, and the elements most like it; a time series
    charted over its last hour, day, week or month, or all of it, as the
    averages of about 500 buckets, with its labels, rules and sizes, and
    samples added to it. Other types, as Bloom filters, say to use the
    console.
  - A Search panel: the server's search indexes, each with what it
    indexes, its fields and its documents; queries run on one, the
    documents found opening as keys; New Index writes FT.CREATE in the
    console to complete, and Drop Index drops one, its keys kept.
  - Values stored compressed or serialized show decoded, read only: gzip,
    zlib, zstd, LZ4, Snappy and Brotli, found by their first bytes or
    chosen; MessagePack, Python's pickle (never running the code it
    names), PHP's serialize, Java's serialization and Protocol Buffers
    without their schema, as JSON or as `protoc --decode_raw` shows
    them, inside the compressions around them. A string decodes whole,
    and a hash's, list's or set's chosen item below its items.
  - TTL, rename, delete and new keys; on Redis 7.4 and later, each hash
    field's TTL, set or removed field by field.
  - Delete every key matching a pattern: counted and named first, always
    confirmed (with the connection's name typed on production), unlinked
    in batches as a scan finds them, and audited.
  - Export the keys matching a pattern to a file of JSON lines, each key
    with its type, its TTL and its value (base64 where it is not text),
    and import such a file: each key written in one transaction, keys
    there already left as they are or replaced, through the safety
    policy, audited. A hash field's own TTL and a stream's consumer
    groups are not exported.
  - A console with history (↑ ↓), which replies the way `redis-cli` does.
    `KEYS` asks first, as it stalls a large server; the tree uses SCAN.
    As a command is typed, the commands it may be show, and Tab completes
    its name; once named, its syntax and summary show, as the server's
    `COMMAND DOCS` gives them (Redis 7 and later).
  - Below the keys, beside the console: a live feed of the commands the
    server runs (MONITOR, on a connection of its own, confirmed on
    production as it slows a busy server; of the connection's database
    or every one, filtered as typed); the slow log, slowest first, with
    a reset of every master's; and an analysis of the keys' memory (asked
    first on production): up to a million keys' type, size and TTL read
    as SCAN finds them, summed into the largest keys, the namespaces
    before the key separator, the types, and when keys expire. Monitored
    commands show as the console's do, their secrets hidden.
  - Pub/Sub, beside them: listen to channels and patterns, the messages
    showing as they come; publish to a channel, through the safety
    policy; and the channels clients listen to, with how many listen, of
    every node of a cluster. Sharded channels (SSUBSCRIBE) are not
    listened to.
  - Run a file of commands, one a line, `#` starting a comment: each one
    through the safety policy first, then, when any writes, once
    confirmed; they run in the console one after the other, audited,
    stopping at the first that fails.

## Charts, diagrams and server activity

- **Charts** of any result: bar, stacked bar, line, area, stacked area,
  scatter, pie, or a histogram of a column's values in bins of round
  bounds, with the axes detected (a time or text column along x, numbers
  as series), a hover tooltip, and a colour-blind-safe palette. Stacks
  put positive values above zero and negative below; a NULL breaks a
  line or an area, and counts as zero in a stack.
- **Server activity:** sessions and running queries of PostgreSQL, MySQL
  and ClickHouse, with cancel (and, on PostgreSQL and MySQL, terminate),
  and Redis's headline metrics and clients, which can be disconnected (of
  one node, in a cluster). Sessions refresh every 2 seconds.
  - Locks (PostgreSQL, MySQL): each lock held or waited for, with the
    sessions a waiting one waits for; MySQL's metadata locks included.
  - Statistics: the statements that took the most time, from
    `pg_stat_statements`, MySQL's `performance_schema`, or ClickHouse's
    `system.query_log` over the last day, with calls, mean and maximum
    times, and rows.
  - Metrics (ClickHouse): queries and merges running, connections,
    memory, parts and load, and queries and rows per second.
- **Users and Privileges** (PostgreSQL, MySQL, ClickHouse): users and
  roles, with what each may do and the roles it belongs to. Make users and
  roles, change a password, grant privileges on a table or on every table
  of a schema, add to a role, revoke a privilege, or drop. Each shows its
  statements and asks first; a password shows nowhere, and on PostgreSQL
  it is hashed (SCRAM-SHA-256) before it is sent, as psql's `\password`
  does. Dropping a PostgreSQL role passes what it owns to you, never
  dropping it.
- **Maintenance** from a table's menu: VACUUM, ANALYZE and REINDEX on
  PostgreSQL, ANALYZE, OPTIMIZE and CHECK on MySQL, ANALYZE and REINDEX on
  SQLite, OPTIMIZE on ClickHouse. Each runs in an editor, through the
  safety policy; manual commit opens no transaction for a statement that
  cannot run in one, as VACUUM.
- **ER diagrams** of a schema: tables with their columns and keys, and
  foreign-key connectors, laid out so referenced tables sit to the left.
  Drag tables to arrange them: one dropped on another moves to the
  nearest free place, so that no table hides another. Double-click one
  to open its data. Save it as a PNG, drawn as the app draws it at twice
  its size, or as SVG.

## Import, snippets and history

- **Copy tables** to another database, of the same engine or another: a
  table's Copy to Another Database…, or a schema's for several. A table
  not there is made with the same columns, nullability and primary key,
  typed as the target holds the values exactly (on the same engine, as
  they were); one there stops the copy, gets the rows added, or has its
  rows replaced. Each table copies in one transaction where the target
  has them, through the target's safety policy, and is audited.
- **Compare the rows** of a table with another's, in the same database or
  another, of any engine: a table's Compare Rows With…. Rows match by the
  target's primary key, over the columns both have, and values compare as
  values: `1.50` and `1.5` are alike, as are `1` and `true`, a time and
  the same time in UTC, and JSON with its keys in another order. It lists
  the rows only in either table and the columns changed, hiding the
  values the grid hides, then makes the target's rows as the source's:
  adding, updating, and only when asked deleting, in one transaction
  through the target's safety policy, each statement changing exactly one
  row, audited with the hidden values masked. Changes without hidden
  values also open as SQL, to read or edit first.
- **Generate test data** for a table: its Generate Test Data… adds up to
  a million rows, each column's values made as its type and name
  suggest, which a column's own settings change: names, e-mails, phone
  numbers, cities, companies, addresses, URLs, words and paragraphs;
  numbers, decimals, dates and times in a range; booleans, UUIDs, JSON; a
  choice of values, an enum's by default; an existing value of the table
  a foreign key refers to; or the column's default, as for auto-increment
  keys. Keys continue from the highest; unique text differs from earlier
  fills' too; text fits its column; a nullable column can take a share of
  NULLs. A sample shows first. The rows are added in one transaction
  where the engine has them, through the safety policy, audited.
 from its menu or its connection's:
  PostgreSQL with `pg_dump`, as an archive or SQL, schema, data or both;
  MySQL with `mysqldump`, in one transaction with its routines, triggers
  and events; SQLite as a copy (`VACUUM INTO`) while it stays in use;
  DuckDB as a folder of Parquet files (`EXPORT DATABASE`). The tools reach
  the server as the connection does, through its tunnel or proxy, their
  password in their environment or a file of their own, never on their
  command line; their progress shows as they write. A pg_dump archive
  restores with `pg_restore` in one transaction, dropping what it makes
  first if asked; a SQL file restores as Run SQL File runs it. A restore
  is always confirmed, the connection's name typed on production, and
  both are audited.
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
- **Snippets:** keep the selected SQL under a name and a keyword, and
  insert it into any editor from the command palette or by typing its
  keyword; `sel`, `ins`, `upd`, `del` and a few more come with every
  editor. A snippet's fields, written `${1:default}`, `$2` and `$0` for
  where the caret ends, are filled in one after the other with Tab.
- **History:** every statement and command is recorded, per project, with
  its timing and error (⌘Y). Open any entry again (↵) in a new editor of
  its connection.
