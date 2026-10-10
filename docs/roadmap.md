# Roadmap and known limitations

## Roadmap

- **Next:**
  - [DEFERRED] Signed installers, and updates installed by the app through MyGo's
    updater.
  - [DEFERRED] Notebook interface.
- **Transactions and safety:**
  - A typed `BEGIN` in an editor under manual commit fails on SQLite and
    DuckDB, as the app has already begun the transaction: take it as that
    transaction's start, or refuse it saying why.
  - On DuckDB and in-memory SQLite, a rename or a drop that fails inside
    the shared transaction aborts it: refuse those changes while a
    transaction is open, as table structure changes are.
  - Manual commit opens a transaction again after an implicit commit:
    follow `COMMIT AND CHAIN`, XA and MariaDB's `BEGIN NOT ATOMIC` too.
  - A run stopped mid-way for another tab's transaction names "another
    session" rather than that tab.
  - The script splitter does not read MariaDB's `/*M! … */` comments, which
    the safety checks do; reading them would change how some scripts split.
  - A panic inside a Redis subscription or MONITOR leaves its reader
    running.
- **To check:**
  - Whether `clickhouse-client --secure`, which ClickHouse backups run,
    verifies the server's certificate.
  - Whether MySQL dumps the app writes load under `NO_BACKSLASH_ESCAPES`,
    as [SQL](sql.md) says.
  - ClickHouse SQL exports loaded back into a ClickHouse server: no test
    does yet.
  - A PostgreSQL statement whose driver deadline fires with no answer from
    the server: is its connection kept, or dropped and the next statement
    refused once?
  - Recovery from a panic while connecting, while Run SQL File reads its
    file, and during an editor's run, end to end: tested only directly.
- **Code:**
  - Background work posts a "done" step whether it returns or panics, so
    that each task clears its busy state once; about 35 state it twice.
  - Every restore plans, shows, then asks, in one order.
  - One way to show a command: the trust prompt, a backup's preview and
    Redis's audit text quote it differently.
  - One reader of `$tag$` strings for sqltext, redact and the editor's
    checks.
  - Containment checks through `filepath.IsLocal`, once its rules are shown
    to match the project's.
  - Resolve the project folder once when restoring a workspace, not once
    for each entry.
  - The audit log and the history each redact a statement; one redaction
    could serve both.
  - Drop `ApplyChange`'s second shared-transaction check once SQLite's
    table rebuild sends its `PRAGMA`s after its `BEGIN`.
  - The editor and Run SQL File follow implicit commits apart, and differ
    on purpose: a file's own `BEGIN … COMMIT` warns with no write before
    the commit.
  - The Linux check for files that tests leave open misses one that Go's
    garbage collector closes during the run (`GOGC=off` shows it).

## Backlog

Features other database clients have that DGopher lacks, from a
comparison in October 2026. Not ordered by priority; items already in
Next or Later are not repeated.

- **Connections:**
  - [DEFERRED] List the databases in an AWS, GCP or Azure account and
    add them as connections.
  - [DEFERRED] Import saved connections from other database clients.
  - [DEFERRED] Group connections in sub-folders or by tags.
- **SQL editor:**
- **Data grid:**
- **Schema:**
- **Import, export and backup:**
- **Compare and migrate:**
- **Test data and automation:**
- **Admin and monitoring:**
- **Redis:**
- **ClickHouse and DuckDB:**
- **Charts:**
- **AI:**
  - [DEFERRED] Write SQL from a description, explain a query, fix an error, and
    convert SQL from one engine's dialect to another's.
  - [DEFERRED] An MCP server that lets coding agents use a connection, asking
    before each kind of access.
- **Interface:**
  - [DEFERRED] Translations: the app is in English only for now.

## Known limitations

- **Kerberos:** PostgreSQL only; MySQL's Kerberos plugin has no Go
  client. On macOS, `KRB5CCNAME` must name a `FILE:` cache: the
  system's default cache is not a file.
- **DuckDB:** a read-only and a read-write connection to the same file
  cannot be open at once: the driver keeps one database per file. A
  column of a table with an index or a key cannot be dropped or retyped,
  from the table form or a model's migration: DuckDB refuses it.
- **ClickHouse:** rows are not editable from the grid, because ClickHouse
  changes rows with asynchronous mutations. Use SQL.
- **Large results:** results keep at most 200,000 rows (`db.MaxRows`).
  Export runs the query again and writes page by page, up to the limit
  it is given.
- **Cancelling behind a proxy:** cancelling a MySQL statement sends
  `KILL QUERY` for the session's connection as MySQL numbers it; ProxySQL
  or RDS Proxy, which share server connections, may number it otherwise.
- **DuckDB transactions:** the open transaction is read with
  `current_transaction_id()`, which DuckDB does not document; a test fails
  if an upgrade changes it.
- **Excel import:** a workbook with more than 4,194,304 shared strings, or
  128 MB of their text, is refused.
- **Trust prompt:** a change to its wording asks once more for
  connections already trusted.
