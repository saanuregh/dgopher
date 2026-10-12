# Audit log

Every action on a database is appended to the `audit` table of
`.dgopher/state.sqlite` in its project. Open the current project's log with ⌘⇧A, from the command
palette, or from a project's menu in the sidebar: it is the Audit Log tab
of the project's History, beside its queries.

**What it records:**
- connects and disconnects;
- every statement run (reads included);
- a SQL file run without an editor: the file's path and SHA-256, how many
  statements ran and how many rows they loaded, and each of its
  statements apart from INSERT and COPY, and each that failed;
- the SQL generated from grid edits, and their outcome;
- imports, exports, cancelled queries and ended sessions;
- Redis commands;
- each confirmation the safety policy asked for, and whether the
  connection's name was typed;
- what the policy refused;
- trusted SSH hosts, and connections added, changed or deleted;
- reads of a table's rows the app makes on its own: a grid's pages,
  counts and value lists, the values test data takes from other tables,
  both tables of a row comparison, the source of a copy, and the value of
  a Redis key opened.

**Traffic:** besides those entries, every statement and Redis command
DGopher itself sends to a server is recorded as it was sent, as
**traffic**: what the app reads to describe a database (schemas, tables,
columns, completions), probes of the server's version and of a session's
transaction, pings, the `BEGIN`, `SAVEPOINT` and `COMMIT` inside a
batch, the server activity and account lists, a Redis key's reads, and
the statements already recorded above, again. A connection renamed, or
given another environment, is recorded under its new name from then on.
Parameter values are not kept, only how many were sent; secrets are
redacted as in other entries, and a statement or Redis argument longer
than 4 KB keeps its first 4 KB and its length. The Audit Log hides
traffic until **Traffic** is ticked. Traffic is written in batches,
within a quarter of a second of being sent and before any other entry
after it, so a crash can lose the last moment of it; what cannot be kept
(more than 10,000 entries waiting, or a write that failed) is counted in
an entry instead.

**Not recorded, as no hook sees it:**
- what a driver sends to connect (Redis `HELLO`, `AUTH` and `SELECT`,
  MySQL's handshake, ClickHouse's first query over HTTP) and to
  disconnect;
- Redis's keep-alive pings, cluster topology reads and Sentinel's
  subscription;
- protocol messages that carry no statement text: MySQL's statement
  prepare and close, PostgreSQL's prepare, deallocate and cancel
  requests, ClickHouse's batch data, and SQLite's rollback after a
  failed commit;
- what `pg_dump`, `pg_restore`, `mysqldump` and the `mysql` client send
  for a backup or restore, which their one entry sums up;
- local DuckDB files the app makes to import, export or restore.

**Who wrote it, and what was sent.** A statement's entry says who wrote
it: **typed**, by a person (in an editor, the console, a SQL or commands
file, a dashboard panel), or **app**, made by the app for what was asked
of it, such as a grid's page, a `BEGIN` before a change is counted, or a
grid edit. The Written by filter shows either. The statement is the text
the server was sent, `EXPLAIN` included when you explained it; when the
app changed what was typed first, writing a `${var}`'s value into it or
turning `:name` into the engine's placeholder, the entry keeps the typed
text too, and the parameters' values apart. A `BEGIN` the app sends of
its own, before a manual-commit editor's first change or to count the
rows a change makes, says why. Grid edits are the exception: their entry shows each edit's
statement with its values written in, and hidden columns' values masked,
where the server got the values apart.

**Each entry holds** the time, the OS user and host, the connection, its
environment, who wrote the statement, rows and duration, and the error if
any; a connect also
records the server's version and whether TLS was used. A failing password
or identity command is recorded as failed with its exit status, without
what it printed.

**Tamper evidence:**
- Every entry carries the SHA-256 of the entry before it, and a number.
- **Verify Integrity** walks the whole chain, in the order written. It
  names the first entry that was changed, removed, inserted or moved, and
  it notices a log whose first entries are missing.
- What a hash chain alone cannot show: entries cut off the end, or a log
  rewritten whole by someone who can write its files. For those, **Copy
  Head** gives the last entry's number and hash. Keep it somewhere else,
  such as a ticket or a change record. Compared later, it shows whether the
  log was rewritten or cut short.
- The log is never trimmed or cleared by the app.
- Each entry is written in one SQLite transaction that reads the last
  entry first, so two DGopher windows on one project keep one chain, and
  a crash cannot leave half an entry.
- A log an older version kept in `audit*.jsonl` files is imported as it
  was: a chain broken there stays broken, at the same entry. A damaged end
  of the last file, from a write cut short, is left out, and an entry says
  so; the files are kept as `*.migrated`.
- Secrets (passwords in `CREATE`/`ALTER USER`, the arguments of functions
  and engines that take credentials, URL and connection-string passwords,
  Redis `AUTH`, `CONFIG SET requirepass` and similar; see
  [How statements are read](safety.md)) are redacted before they are
  written.
- **Export** saves the whole log to a file of your choice, one JSON entry
  per line, oldest first. The exported file carries the same chain, so it
  can be checked on its own. Entries written before an entry said who
  wrote it hash as they did; a DGopher older than that reads newer entries
  as changed.
