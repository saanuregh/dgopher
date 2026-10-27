# Engine support

What each engine supports in DGopher. CockroachDB, YugabyteDB and
TimescaleDB are engines of PostgreSQL's family, and MariaDB and TiDB of
MySQL's: each supports what its family's engine does in the tables
below, except where its own section under
[Engines of a family](#engines-of-a-family) says otherwise. [Features](features.md) describes each feature, and
[Known limitations](limitations.md) explains the gaps.

- ✓ supported
- — not supported, or does not apply

## Connecting

| | PostgreSQL | MySQL | SQL Server | ClickHouse | SQLite | DuckDB | Redis |
| --- | --- | --- | --- | --- | --- | --- | --- |
| TLS (prefer, require, verify-full) | ✓ | ✓ | ✓ | ✓ | — | — | ✓ |
| SSH tunnel | ✓ | ✓ | ✓ | ✓ | — | — | ✓ |
| SOCKS5 or HTTP proxy | ✓ | ✓ | ✓ | ✓ | — | — | ✓ |
| Cloud IAM (AWS, Google Cloud, Entra ID) | ✓ | ✓ | Entra ID | — | — | — | — |
| Kerberos | ✓ | — | ✓ | — | — | — | — |
| Clear-text password (LDAP, PAM) | — | ✓ | — | — | — | — | — |
| Several databases on one connection | ✓ | ✓ (as schemas) | ✓ | ✓ (as schemas) | — | — | — |
| In memory | — | — | — | — | ✓ | ✓ | — |
| Cluster or Sentinel | — | — | — | — | — | — | ✓ |
| Idle connections disconnect | ✓ | ✓ | ✓ | ✓ | — | — | ✓ |

## Safety and transactions

| | PostgreSQL | MySQL | SQL Server | ClickHouse | SQLite | DuckDB | Redis |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Transactions and manual commit | ✓ | ✓ | ✓ | — | ✓ | ✓ | — |
| Savepoints | ✓ | ✓ | — | — | ✓ | — | — |
| Schema changes inside a transaction | ✓ | — | ✓ | — | ✓ | ✓ | — |
| Read-only enforced by the server | ✓ | ✓ | — (the app's check only) | ✓ | ✓ | ✓ | — (the app's check only) |
| Large changes held for confirmation | ✓ | ✓ | ✓ | — | ✓ (files, not in memory) | — | — |
| Idle transactions roll back | ✓ | ✓ | ✓ | — | ✓ | ✓ | — |
| One connection shared by all tabs | — | — | — | — | in memory | ✓ | — |

DuckDB and in-memory SQLite share one connection, so all their tabs share
one transaction; see [Safety model](safety.md#duckdb-and-in-memory-sqlite).

## Querying

| | PostgreSQL | MySQL | SQL Server | ClickHouse | SQLite | DuckDB |
| --- | --- | --- | --- | --- | --- | --- |
| Parameters | `:name` | `:name` | `:name` | `:name`, `{name:Type}` | `:name`, `@name`, `$name` | `:name` |
| Explain | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| Explain Analyze | ✓ | ✓ | ✓ | — | — | ✓ |
| Plan advice | ✓ | ✓ | ✓ | ✓ | ✓ | — |
| Schema switch in the editor | `SET search_path` | `USE` | `USE` (a database) | `USE` | — | `USE` |
| Edit rows in the grid | ✓ | ✓ | ✓ | — | ✓ | ✓ |

`${var}` variables work on every SQL engine.

## Schema

| | PostgreSQL | MySQL | SQL Server | ClickHouse | SQLite | DuckDB |
| --- | --- | --- | --- | --- | --- | --- |
| Create tables in the table form | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| Change tables in the table form | ✓ | ✓ | ✓ | columns only | ✓ (rebuilt when needed) | columns only |
| ER diagram | ✓ | ✓ | ✓ | view only (no foreign keys) | ✓ | ✓ |
| Data models and DDL | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| Partitions | ✓ | — | — | — | — | — |
| Other objects | functions, procedures, triggers, sequences, types, extensions | functions, procedures, triggers, events | procedures, functions, triggers, sequences, synonyms, types | projections | triggers | macros, sequences, types |

## Data

| | PostgreSQL | MySQL | SQL Server | ClickHouse | SQLite | DuckDB |
| --- | --- | --- | --- | --- | --- | --- |
| Import files | ✓ | ✓ | ✓ | ✓ (not in one transaction) | ✓ | ✓ |
| Export | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| Copy tables, compare rows, test data | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| Backup | `pg_dump` (archive or SQL) | `mysqldump` (SQL) | BACPAC (`sqlpackage`), or a backup file on the server (`BACKUP DATABASE`) | — | a copy (`VACUUM INTO`) | Parquet folder (`EXPORT DATABASE`) |
| Restore | ✓ | ✓ | ✓ | — | ✓ | ✓ |

Redis has its own key [export and import](features.md#keys).

## Administration

| | PostgreSQL | MySQL | SQL Server | ClickHouse | SQLite | DuckDB | Redis |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Sessions, with cancel | ✓ | ✓ | ✓ (ending them only) | ✓ | — | — | clients |
| End a session | ✓ | ✓ | ✓ (`KILL`) | — | — | — | disconnect a client |
| Locks | ✓ | ✓ | ✓ | — | — | — | — |
| Slowest statements | ✓ (`pg_stat_statements`) | ✓ (`performance_schema`) | ✓ (`sys.dm_exec_query_stats`) | ✓ (`system.query_log`) | — | — | slow log |
| Live metrics | — | — | — | ✓ | — | — | ✓ |
| Users and privileges | ✓ | ✓ | ✓ (logins and users) | ✓ | — | — | — |
| Maintenance | VACUUM, VACUUM ANALYZE, VACUUM FULL, ANALYZE, REINDEX | ANALYZE, OPTIMIZE, CHECK | UPDATE STATISTICS, index REORGANIZE and REBUILD, DBCC CHECKTABLE | OPTIMIZE, OPTIMIZE FINAL | ANALYZE, REINDEX | — | — |

The slowest-statements page needs the server to collect them: the
`pg_stat_statements` extension on PostgreSQL, `performance_schema` on
MySQL, and query logging on ClickHouse; on SQL Server the activity pages
need the `VIEW SERVER STATE` permission. The page says how to turn each on.

## Engines of a family

### MariaDB

Tested against MariaDB 11.4; supported from 10.11, the oldest MariaDB
release still maintained.

- **Explain** uses `EXPLAIN FORMAT=JSON`, and **Explain Analyze**
  `ANALYZE FORMAT=JSON`, which runs the statement; both show the same
  tree of steps with rows and times, and the advice also flags block
  nested-loop joins. `ANALYZE` of a statement is checked as that
  statement: of a `SELECT` it is a read, of an `UPDATE` a write.
- **Read-only** connections set `tx_read_only`, which MariaDB 10.11 has
  (`transaction_read_only` came in 11.1).
- `NEXTVAL`, `SETVAL` and `LASTVAL`, and `NEXT VALUE FOR`, count as
  writes.
- **Locks** lists InnoDB row locks waited on, and the transactions waiting
  behind them (`information_schema.INNODB_LOCK_WAITS`); it needs the
  `PROCESS` privilege and shows no metadata locks.
- **Slowest statements** needs the server started with
  `performance_schema=ON`, which MariaDB leaves off.
- **Users and privileges**: roles have no host, and grants are shown
  without password hashes.
- **Other objects** adds sequences. A system-versioned table is a table
  whose engine reads "InnoDB, system-versioned", and a JSON column (a
  `LONGTEXT` checked by `JSON_VALID`) shows the type `json`.
- **Backup** runs `mariadb-dump`, or `mysqldump` when it is not
  installed; through an SSH tunnel or proxy, verify-full TLS checks the
  server's name, which `mariadb-dump` cannot be told to skip.

### TiDB

Tested against TiDB 8.5; supported from 7.5, the oldest TiDB release
PingCAP still maintains (until 2026-12-01; 8.1 then).

- **Read-only** is the app's check only: TiDB refuses the read-only
  setting, so the connection opens without it and the app refuses writes. A
  read-only connection saved as MySQL that reaches a TiDB server fails to
  open, and its error says to set its engine to TiDB.
  Dashboards run their queries without a read-only transaction.
- **Explain** and **Explain Analyze** use `FORMAT='tidb_json'`; the plan
  shows TiDB's operators, with its own advice on full table scans and
  sorts.
- **Sessions** span every TiDB server, with a column naming it; **Locks**
  shows the transactions waiting and those they wait for; **Slowest
  statements** reads the statement summary, for its current window.
  **Stop** and **End** do nothing when the cluster's `enable-global-kill`
  is off.
- A **write conflict** (error 9007, at commit in optimistic mode) says the
  transaction clashed and can be run again; the app never retries it.
- **Other objects** has sequences, and no routines, triggers or events,
  which TiDB lacks. CHECK constraints are kept only when the cluster has
  `tidb_enable_check_constraint` on. A clustered primary key cannot be
  changed, and a foreign key keeps an index of its own.
- **Backup** writes the schema only, by `mysqldump`: TiDB cannot give a
  dump a consistent snapshot of the data.

### CockroachDB

Supported from 26.2, the older of the two newest CockroachDB releases.

- **Read-only** is enforced by the server (`default_transaction_read_only`),
  and the app also refuses what CockroachDB still runs on a read-only
  session: `CREATE STATISTICS`, `EXPLAIN ANALYZE (DEBUG)`, any
  `crdb_internal` function but known reads, a `SET search_path` that
  names `crdb_internal`, and a `SET` or `RESET` of
  `allow_unsafe_internals`, which opens `crdb_internal` to the session. A
  `[ <statement> ]` data source counts as the statement it runs.
- **Transactions** are serializable: a statement that clashes with another
  transaction fails with SQLSTATE 40001, which says to roll back and run
  it again from its start; the app never retries it. A schema change commits the open transaction
  first (CockroachDB's `autocommit_before_ddl`), so the app warns as it
  does for MySQL, and schema scripts run statement by statement.
  `SET CLUSTER SETTING`, `BACKUP`, `RESTORE`, `IMPORT`, `EXPORT` and
  `CREATE CHANGEFEED` run outside a transaction, and count as changing
  the server.
- **Explain** and **Explain Analyze** use `EXPLAIN (VERBOSE)`, read as a
  tree; the advice flags full scans and repeats CockroachDB's own index
  recommendations.
- **Catalog** reads only what CockroachDB supports reading: `pg_catalog`,
  `information_schema` and `SHOW` statements, never the `crdb_internal`
  tables CockroachDB closes to sessions by default. Estimated rows come
  from table statistics (about ten seconds after `ANALYZE`); sizes come
  from `SHOW RANGES`, read once for the database's ranges each time a
  schema's objects are listed, need the `VIEWACTIVITY` or `ZONECONFIG`
  privilege, and are blank without it; hidden columns such as `rowid` are
  left out; there are no partitions, domains, range types or extensions.
  Changing a primary key is one statement, as CockroachDB requires; the
  table designer cannot remove one.
- **Activity**: sessions with Cancel Query and End Session, locks (read
  when asked, as they are costly) and statement statistics, kept per hour;
  they need `VIEWACTIVITY`. A **Jobs** page lists `SHOW JOBS`: those
  running and those of the last 12 hours.
- **Errors** point at the place CockroachDB's message marks, since it
  gives no position.
- **Backup** writes the schema only, by `pg_dump`, and needs CockroachDB
  26.3 or later with `pg_dump` 18; restore runs the file with Run SQL
  File. Run by a user other than `root`, the restore stops at the dump's
  `REVOKE ALL ON SCHEMA public FROM root`, near its end, after the tables
  are made. **Maintenance** offers Analyze.

### YugabyteDB

Supported from 2024.2, the oldest YugabyteDB release still maintained.

- **Read-only**, savepoints, users and grants work as on PostgreSQL. A
  statement that clashes with another Repeatable Read or Serializable
  transaction fails with SQLSTATE 40001, which says it can be run again;
  the app never retries it.
- **Schema changes** and `TRUNCATE` are not part of the open transaction:
  each one takes effect at once, and Roll Back does not undo it, so the
  app warns before one runs inside an open transaction, and manual commit
  opens none for it. Run SQL File does not offer "in one transaction", and
  the table form's changes are not all-or-nothing. `CREATE TABLEGROUP`,
  and `CREATE INDEX` without `NONCONCURRENTLY`, run outside a transaction:
  inside one, YugabyteDB builds an index without online backfill.
- **Explain Analyze** adds `DIST`: steps show the filters YugabyteDB runs
  in storage, with storage reads and times, and the advice weighs rows
  read in storage against rows kept.
- **Sizes** count each table's secondary indexes; tables of a colocated
  database show no size, and a new table may show 0 until storage writes
  it.
- **Activity**: Sessions, Statistics and Wait Events cover only the node
  the connection reached. Locks cover the cluster, for transactions open
  at least a second (`yb_locks_min_txn_age`), at most 16 of them; a lock
  held on another node names its transaction, not a process, and Cancel
  Query and End Session refuse it: `yb_cancel_transaction` with the ID in
  its transaction column cancels it. Cancel Query and End Session on a
  statement waiting for a row lock take effect when the wait ends: end the
  holder's session to free it. On 2024.2, Wait Events needs the server's
  `ysql_yb_enable_ash` flag.
- **Maintenance** offers Analyze: YugabyteDB's VACUUM does nothing, and it
  has no REINDEX or CLUSTER.
- **Backup** runs `ysql_dump` when it is installed, which keeps
  YugabyteDB's key order and colocation, else `pg_dump`. Under
  verify-full without a CA file, `ysql_dump`, of PostgreSQL 15 or older,
  cannot check the certificate: `pg_dump` 16 or later backs up instead,
  or the connection needs its CA file. The app asks before `pg_dump` 17
  or later writes an archive, as it restores only into PostgreSQL.
  Restore runs a statement at a time and stops at the first error,
  without "drop first"; stopping it, or an error, keeps what was restored
  so far. It needs `pg_restore` 16 or earlier, or YugabyteDB's own.
- `yb_` functions count as writes, except those known only to read, and
  so does a `SET` or `RESET` of `yb_disable_transactional_writes` or
  `yb_enable_upsert_mode`.
- `LISTEN` and `NOTIFY` are accepted and ignored.

### TimescaleDB

TimescaleDB 2, tested on 2.31 and checked from 2.13; Timescale publishes
no end of life for its 2.x releases. A database without the extension
works as PostgreSQL does.

- The navigator and Search Objects leave out TimescaleDB's own schemas
  (`_timescaledb_*`, `timescaledb_information`,
  `timescaledb_experimental`).
- A **hypertable** is a table marked `hypertable` (or `hypertable,
  compressed`), with its size and estimated rows over all its chunks; its
  definition adds `create_hypertable`, its other dimensions and its
  compression settings. A **continuous aggregate** is a materialized view
  marked `continuous aggregate`, whose definition is its own query,
  created `WITH NO DATA`. When TimescaleDB's catalog cannot be read, the
  schema lists as PostgreSQL's, unmarked, and a definition ends with a
  comment saying what it lacks.
- Each schema has **Chunks**, the newest (`timescaleChunkItems` in
  `internal/db/timescaledb.go`), with each chunk's range and whether it is
  compressed, and an item saying how many more there are, which
  `timescaledb_information.chunks` lists; and **Jobs**: policies and
  `add_job` jobs, each defined by the call that creates it. Activity adds
  a **Jobs** page with their last and next runs.
- Plans name the `ChunkAppend` and `ColumnarScan` steps, as PostgreSQL's
  name any extension's custom scan, and show their vectorized filters.
- These run outside a transaction, as TimescaleDB requires: calls of
  `refresh_continuous_aggregate`, `merge_chunks_concurrently`, `run_job`
  (whose job may commit as it goes), `reorder_chunk` and `move_chunk`, by
  quoted names too; a continuous aggregate created with its data; and
  `CREATE INDEX … WITH (timescaledb.transaction_per_chunk)`.
- On a **read-only** connection the app refuses TimescaleDB's functions
  that change something, and `SET` or `RESET` of
  `timescaledb.restoring`; the server itself still runs `reorder_chunk`,
  `remove_all_policies`, its internal functions and that `SET`, so for
  those the app's check is the only one.
- **Restore** runs between `public.timescaledb_pre_restore()` and
  `public.timescaledb_post_restore()` (of the extension's schema), each on
  a connection of its own, without "drop first", which TimescaleDB's own
  objects refuse; the server needs the TimescaleDB version the backup was
  made with. It needs a **superuser**: `timescaledb_pre_restore()` sets
  `timescaledb.restoring` on the database and stops its background
  workers, which TimescaleDB 2.31 refuses any other user, even one
  granted `SET` on the parameter. Should `timescaledb_post_restore()`
  fail, the app resets the setting and restarts the workers itself
  (`ALTER DATABASE … RESET timescaledb.restoring`, `SELECT
  _timescaledb_functions.restart_background_workers()`), or says to run
  them; and the connections opened during the restore are not used again.
  An editor opened on that database while the restore ran keeps
  TimescaleDB switched off until its tab closes: close it afterwards.
  A SQL file restored with Run SQL File needs `SELECT
  public.timescaledb_pre_restore();` first and `SELECT
  public.timescaledb_post_restore();` last.

## SQL Server

SQL Server 2019 and later, and Azure SQL Database; tested on SQL Server
2025.

- **Connecting**: TLS off and prefer encrypt the login, without checking
  the certificate, and the rest only when the server forces encryption:
  without TLS the password would cross the network merely scrambled. A
  server that offers no encryption at all gets the login unencrypted, its
  password scrambled only, in these two modes; require and verify-full
  refuse it. Require encrypts everything without checking the
  certificate; verify-full checks it. A
  named instance (`host\instance`) and TDS 8 (`encrypt=strict`) are not
  supported. Azure SQL logs in with an Entra ID token from the `az` CLI.
  Leaving the user empty logs in with Kerberos (the `krb5` ticket cache on
  Linux and macOS, the Windows login on Windows); `DOMAIN\user` logs in
  with NTLM. URLs: `sqlserver://user@host:1433?database=shop`, or
  `mssql://`.
- **Batches**: the editor runs a T-SQL batch at a time, up to a `GO` line
  (or a blank line, with that setting on), so variables last through it;
  see [SQL](sql.md#t-sql-batches). `PRINT` and `RAISERROR` messages show
  with the results, and every result set of a batch shows, each up to the
  row limit. A batch that writes shows its row count, not its rows. A
  `CREATE VIEW`, procedure, function or trigger must be alone in its
  batch. After a batch whose `USE` moved the session to another
  database, the editor says so, and the rows read there are not edited
  until the session is back in the editor's database.
- **Read-only** is the app's check only: SQL Server has no read-only
  session (`ApplicationIntent=ReadOnly` does nothing on a primary).
- **Transactions**: schema changes are part of the transaction. The app
  reads the open transaction from the server after each run, nested
  `BEGIN TRAN`s and `SET IMPLICIT_TRANSACTIONS ON` included; a `COMMIT`
  with two open leaves one open. The app's own commit, of manual commit
  or of grid edits, also commits the implicit transaction its `BEGIN
  TRANSACTION` nested in under `IMPLICIT_TRANSACTIONS ON`; inside a
  transaction you began, it fails, saying nothing is committed until
  that one is. Savepoints are not used: under
  `XACT_ABORT ON` a failed statement dooms the whole transaction, so grid
  edits ask you to end an open transaction first. `CREATE`, `ALTER` and
  `DROP DATABASE`, `BACKUP`, `RESTORE`, `RECONFIGURE`, `KILL`, full-text
  catalogs and `DBCC SHRINKDATABASE` run outside a transaction, and
  `UPDATE STATISTICS` takes effect at once. Under manual commit a batch
  is read statement by statement: `UPDATE STATISTICS` among writes runs in
  the transaction opened for them, after asking; a statement that runs
  outside a transaction among writes asks first, as no transaction is
  opened for that batch. On staging and production, with a change limit
  set, a batch that makes several changes, or that changes rows and
  begins or ends its own transaction, asks first: the limit cannot count
  its rows before they run or are committed. Grid edits
  count the rows each change made itself, not those of the triggers it
  fired. A deadlock victim (1205) or a
  snapshot update conflict (3960) says the transaction can be run again;
  the app never retries it.
- **Explain** runs the batch between `SET SHOWPLAN_XML ON` and `OFF`,
  which plans it without running it; **Explain Analyze** between `SET
  STATISTICS XML ON` and `OFF`, which runs it and shows its rows and plan.
  The `OFF` always follows, after an error or a cancel too; if it fails,
  the connection is closed and the next statement says so. Explaining a
  write is checked as that write, so a read-only connection refuses it.
  Advice includes SQL Server's own missing-index suggestions, scans that
  keep few rows, heavy key lookups, spills to `tempdb`, joins without a
  predicate and conversions that change the plan.
- **Values**: `uniqueidentifier` shows as its usual text, `datetimeoffset`
  with its offset, `datetime2` to the microsecond, `money` and `decimal`
  exactly. Table tabs show `geography` and `geometry` as WKT and
  `hierarchyid` as its path, and edit them as text; a query shows their
  bytes unless it converts them (`.STAsText()`, `.ToString()`).
- **Table form**: comments are `MS_Description` properties; renames run
  `sp_rename`; changing a column's type drops and adds back its default
  constraint, and resets its collation to the database's.
- **Users and privileges** lists logins with their user in the database,
  users without a login, and server and database roles; a new account is
  a login and a user of the same name.
- **Activity**: Sessions, Locks and Statistics. **End Session** runs
  `KILL`; SQL Server cannot stop another session's query without ending
  the session.
- **Backup** writes a BACPAC with `sqlpackage` when it is installed (on
  Linux it needs ICU; it cannot use the connection's CA file, and a
  password with `"` is refused, as `sqlpackage`'s argument file cannot
  hold one), or a backup file on the **server**, at a path the server
  sees. **Restore** of a server backup file runs on `master`, in one
  batch: it takes the database offline, which ends its other sessions and
  keeps new ones out, and restores over it, which brings it online. A
  restore that fails before replacing the database brings it back online
  as it was; one that leaves it restoring says so, to restore again or
  drop it. Restore is refused while one of the app's tabs holds an open
  transaction on the connection. A BACPAC
  restores into an empty database. A failed or stopped restore leaves the
  database restoring.

