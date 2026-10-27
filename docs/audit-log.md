# Audit log

DGopher records every action on a database in an append-only,
tamper-evident log, one per project.

## Open it

- Press ⌘⇧A (Ctrl+Shift+A on Linux and Windows).
- Or choose **Audit Log** from the command palette, the View menu, or a
  project's menu in the sidebar.

It opens as the Audit Log tab of the project's History dialog, beside the
Queries tab (⌘Y).

![The Audit Log tab: statements run, a confirmation, and the grid edit it
let through, each with its kind, who wrote it, and the connection](images/audit-log.png)

The log is the `audit` table of `.dgopher/state.sqlite` in the project
folder ([Where things are kept](storage.md)).

## What it records

- Connects and disconnects. A connect also records the server's version
  and whether TLS was used.
- Every statement run, reads included.
- The SQL generated from grid edits, and its outcome.
- A SQL file run without an editor: the file's path and SHA-256, how many
  statements ran and how many rows they loaded, each statement other than
  `INSERT` and `COPY`, and each statement that failed.
- Imports, exports, backups and restores.
- Cancelled queries and ended sessions.
- Redis commands.
- Each confirmation the safety policy asked for, and whether the
  connection's name was typed.
- What the policy refused.
- Trusted SSH hosts, and connections added, changed or deleted.
- Reads of a table's rows that the app makes on its own: a grid's pages,
  counts and value lists, the values test data takes from other tables,
  both tables of a row comparison, the source of a copy, and the value of
  a Redis key you open.
- A failing password or identity command, with its exit status but not
  what it printed.

### Traffic

Besides those entries, the log records every statement and Redis command
that DGopher itself sends to a server, exactly as sent. These entries are
marked **traffic**. They include:

- what the app reads to describe a database: schemas, tables, columns and
  completions;
- probes of the server's version and of a session's transaction state, and
  pings;
- the `BEGIN`, `SAVEPOINT` and `COMMIT` inside a batch;
- the server activity and account lists, and a Redis key's reads;
- the statements already recorded above, again.

Some drivers end a transaction or check a connection by a call that
carries no statement, as Oracle's driver, go-ora, commits, rolls back and
pings. Such a call is recorded as the statement it stands for: `COMMIT`,
`ROLLBACK`, or `-- ping`. An Oracle connection's settings, its
`ALTER SESSION` statements, are recorded as it opens, and a session's
reads of its transaction and of `DBMS_OUTPUT` after each statement.

How traffic is kept:

- Parameter values are not kept, only how many were sent.
- Secrets are redacted, as in other entries.
- A statement or Redis argument longer than 4 KB keeps its first 4 KB and
  its full length.
- Traffic is written in batches: within a quarter of a second, or as soon
  as 256 entries wait, and always before the next non-traffic entry. A
  crash can lose the last moment of traffic.
- If more than 10,000 entries wait, or a write fails, the entries that
  could not be kept are counted in an entry instead.
- A connection that is renamed, or given another environment, is recorded
  under its new name from then on.

### Not recorded

No hook in the app sees these, so they are not in the log:

- What a driver sends to connect and disconnect: Redis `HELLO`, `AUTH` and
  `SELECT`, MySQL's handshake, and ClickHouse's first query over HTTP.
- Redis keep-alive pings, cluster topology reads and Sentinel's
  subscription.
- Protocol messages that carry no statement text: MySQL's statement
  prepare and close, PostgreSQL's prepare, deallocate and cancel requests,
  ClickHouse's batch data, and SQLite's rollback after a failed commit.
- What `pg_dump`, `pg_restore`, `mysqldump`, the `mysql` client, `expdp`
  and `impdp` send for a backup or restore. One entry sums each of them up.
- Local DuckDB files the app creates to import, export or restore.

## What an entry holds

Each entry holds:

- the time, the OS user and host;
- the connection and its environment;
- the kind of action, and who wrote the statement (below);
- the statement or command, redacted;
- the rows affected and the duration;
- the error, if any.

**Who wrote it.** A statement is either:

- **typed** by a person, in an editor, the Redis console, a SQL or command
  file, or a dashboard panel; or
- made by the **app** for something you asked, such as a grid's page, a
  `BEGIN` before a change is counted, or a grid edit.

**What was sent.** The statement is the text the server received,
including `EXPLAIN` when you explained it. An engine that explains in
steps, as SQL Server's `SET SHOWPLAN_XML ON` and `OFF`, or Oracle's read
of `PLAN_TABLE` after `EXPLAIN PLAN`, records each step as the app's, for
Explain. When the app changed what you
typed first, for example by writing a `${var}`'s value into it or turning
`:name` into the engine's placeholder, the entry also keeps your typed
text, and the parameter values separately. A `BEGIN` that the app sends on
its own, before a manual-commit editor's first change or to count the rows
a change makes, says why.

Grid edits are the exception: their entry shows each edit's statement with
its values written in, and the values of hidden columns masked, even
though the server received the values separately, as parameters.

## The Audit Log tab

- The tab shows the newest 5,000 entries.
- Filter by text (statement, connection, user or error), by kind, and by
  **Written by**: anyone, typed, or app.
- Traffic is hidden until you tick **Traffic**.

## Tamper evidence

- Every entry carries a sequence number and the SHA-256 of the entry before
  it, forming a hash chain.
- **Verify Integrity** walks the whole chain in the order written. It names
  the first entry that was changed, removed, inserted or moved, and notices
  a log whose first entries are missing.
- A hash chain alone cannot show entries cut off the end, or a log
  rewritten whole by someone who can write its files. For those, **More ›
  Copy Head** copies the last entry's number and hash. Keep that somewhere else,
  such as a ticket or a change record. Compared later, it shows whether the
  log was rewritten or cut short.
- The app never trims or clears the log. Clearing the query history leaves
  it as it is.
- Each entry is written in one SQLite transaction that reads the last entry
  first. Two DGopher windows on one project keep one chain, and a crash
  cannot leave half an entry.
- Secrets are redacted before they are written. See
  [How SQL is read](sql.md#secrets) for what counts as a secret.

## Export

**More › Export…** saves the whole log to a file you choose
(`dgopher-audit-<date>.jsonl` by default): one JSON entry per line, oldest
first. The file carries the same chain, so it can be verified on its own.

Entries from before the Written by field still verify. A DGopher from
before that field reports newer entries as changed.

## Logs from older versions

Older versions kept the log in `.dgopher/audit.jsonl` and its rotated
`audit-<n>.jsonl` files. They are
imported as they were, so a chain broken there stays broken at the same
entry. A damaged end of the last file, from a write cut short, is left out,
and an entry says so. The old files are kept as `*.migrated`.
