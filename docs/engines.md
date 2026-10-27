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

| | PostgreSQL | MySQL | SQL Server | Oracle | ClickHouse | SQLite | DuckDB | Redis |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| TLS (prefer, require, verify-full) | ✓ | ✓ | ✓ | require, verify-full | ✓ | — | — | ✓ |
| SSH tunnel | ✓ | ✓ | ✓ | ✓ | ✓ | — | — | ✓ |
| SOCKS5 or HTTP proxy | ✓ | ✓ | ✓ | ✓ | ✓ | — | — | ✓ |
| Cloud IAM (AWS, Google Cloud, Entra ID) | ✓ | ✓ | Entra ID | — | — | — | — | — |
| Kerberos | ✓ | — | ✓ | — | — | — | — | — |
| Clear-text password (LDAP, PAM) | — | ✓ | — | — | — | — | — | — |
| Several databases on one connection | ✓ | ✓ (as schemas) | ✓ | — (a service each) | ✓ (as schemas) | — | — | — |
| In memory | — | — | — | — | — | ✓ | ✓ | — |
| Cluster or Sentinel | — | — | — | — | — | — | — | ✓ |
| Idle connections disconnect | ✓ | ✓ | ✓ | ✓ | ✓ | — | — | ✓ |

## Safety and transactions

| | PostgreSQL | MySQL | SQL Server | Oracle | ClickHouse | SQLite | DuckDB | Redis |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Transactions and manual commit | ✓ | ✓ | ✓ | ✓ | — | ✓ | ✓ | — |
| Savepoints | ✓ | ✓ | — | — | — | ✓ | — | — |
| Schema changes inside a transaction | ✓ | — | ✓ | — | — | ✓ | ✓ | — |
| Read-only enforced by the server | ✓ | ✓ | — (the app's check only) | from 23ai (the app's check only before) | ✓ | ✓ | ✓ | — (the app's check only) |
| Large changes held for confirmation | ✓ | ✓ | ✓ | ✓ | — | ✓ (files, not in memory) | — | — |
| Idle transactions roll back | ✓ | ✓ | ✓ | ✓ | — | ✓ | ✓ | — |
| One connection shared by all tabs | — | — | — | — | — | in memory | ✓ | — |

DuckDB and in-memory SQLite share one connection, so all their tabs share
one transaction; see [Safety model](safety.md#duckdb-and-in-memory-sqlite).

## Querying

| | PostgreSQL | MySQL | SQL Server | Oracle | ClickHouse | SQLite | DuckDB |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Parameters | `:name` | `:name` | `:name` | `:name`, `:1` | `:name`, `{name:Type}` | `:name`, `@name`, `$name` | `:name` |
| Explain | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| Explain Analyze | ✓ | ✓ | ✓ | ✓ | — | — | ✓ |
| Plan advice | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — |
| Schema switch in the editor | `SET search_path` | `USE` | `USE` (a database) | `ALTER SESSION SET CURRENT_SCHEMA` | `USE` | — | `USE` |
| Edit rows in the grid | ✓ | ✓ | ✓ | ✓ | — | ✓ | ✓ |

`${var}` variables work on every SQL engine.

## Schema

| | PostgreSQL | MySQL | SQL Server | Oracle | ClickHouse | SQLite | DuckDB |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Create tables in the table form | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| Change tables in the table form | ✓ | ✓ | ✓ | ✓ | columns only | ✓ (rebuilt when needed) | columns only |
| ER diagram | ✓ | ✓ | ✓ | ✓ | view only (no foreign keys) | ✓ | ✓ |
| Data models and DDL | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| Partitions | ✓ | — | — | — | — | — | — |
| Other objects | functions, procedures, triggers, sequences, types, extensions | functions, procedures, triggers, events | procedures, functions, triggers, sequences, synonyms, types | packages, procedures, functions, triggers, sequences, synonyms, types | projections | triggers | macros, sequences, types |

## Data

| | PostgreSQL | MySQL | SQL Server | Oracle | ClickHouse | SQLite | DuckDB |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Import files | ✓ | ✓ | ✓ | ✓ | ✓ (not in one transaction) | ✓ | ✓ |
| Export | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| Copy tables, compare rows, test data | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| Backup | `pg_dump` (archive or SQL) | `mysqldump` (SQL) | BACPAC (`sqlpackage`), or a backup file on the server (`BACKUP DATABASE`) | Data Pump (`expdp`), a dump file on the server | — | a copy (`VACUUM INTO`) | Parquet folder (`EXPORT DATABASE`) |
| Restore | ✓ | ✓ | ✓ | ✓ (`impdp`) | — | ✓ | ✓ |

Redis has its own key [export and import](features.md#keys).

## Administration

| | PostgreSQL | MySQL | SQL Server | Oracle | ClickHouse | SQLite | DuckDB | Redis |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Sessions, with cancel | ✓ | ✓ | ✓ (ending them only) | ✓ (`ALTER SYSTEM CANCEL SQL`) | ✓ | — | — | clients |
| End a session | ✓ | ✓ | ✓ (`KILL`) | ✓ (`KILL SESSION`) | — | — | — | disconnect a client |
| Locks | ✓ | ✓ | ✓ | ✓ | — | — | — | — |
| Slowest statements | ✓ (`pg_stat_statements`) | ✓ (`performance_schema`) | ✓ (`sys.dm_exec_query_stats`) | ✓ (`V$SQLSTATS`) | ✓ (`system.query_log`) | — | — | slow log |
| Live metrics | — | — | — | — | ✓ | — | — | ✓ |
| Users and privileges | ✓ | ✓ | ✓ (logins and users) | ✓ (users and roles) | ✓ | — | — | — |
| Maintenance | VACUUM, VACUUM ANALYZE, VACUUM FULL, ANALYZE, REINDEX | ANALYZE, OPTIMIZE, CHECK | UPDATE STATISTICS, index REORGANIZE and REBUILD, DBCC CHECKTABLE | statistics (`DBMS_STATS`), MOVE, SHRINK SPACE, VALIDATE STRUCTURE | OPTIMIZE, OPTIMIZE FINAL | ANALYZE, REINDEX | — | — |

The slowest-statements page needs the server to collect them: the
`pg_stat_statements` extension on PostgreSQL, `performance_schema` on
MySQL, and query logging on ClickHouse; on SQL Server the activity pages
need the `VIEW SERVER STATE` permission, and on Oracle
`SELECT_CATALOG_ROLE` (see [Oracle](#oracle)). The page says how to turn
each on.

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

## Oracle

Oracle Database 19c and later; tested on Oracle AI Database 26ai Free
(version 23.26, the release 23ai was renamed to). The driver is go-ora,
which needs no Oracle client installed.

- **Connecting**: a connection reaches one service, as a pluggable
  database's (`FREEPDB1`), or an instance by its SID. Each pluggable
  database is a connection of its own; its schemas are listed as the
  schemas of the connection. TLS is disable, require or verify-full:
  prefer is refused, as an Oracle listener speaks TCPS or plain TCP on a
  port of its own. Verify-full checks the certificate against the host
  the connection names, through an SSH tunnel or a proxy too. A
  **wallet** is a folder holding an auto-login wallet (`cwallet.sso`): the
  app trusts its certificates and sends its client certificate, for a
  listener that asks for one; a client certificate's PEM files do the
  same. A wallet that only a password opens (`ewallet.p12`) is not read.
  Kerberos is not offered. URLs:
  `oracle://user@host:1521/service`, with `?SID=…` instead of a service,
  and `SSL`, `SSL VERIFY` and `WALLET` as go-ora writes them; any other
  parameter is refused. From URL also reads a connect descriptor,
  `(DESCRIPTION=…)` as `tnsnames.ora` holds one, with a single address.
- **Statements**: the editor runs one SQL statement or one PL/SQL unit at a
  time, split at `;`, at a line holding only `/`, and around PL/SQL
  blocks; see [SQL](sql.md#oracle-scripts). What PL/SQL prints with
  `DBMS_OUTPUT` shows with the results, as messages, once the statement
  ends.
- **Read-only**: from 23ai the app sets `READ_ONLY` on each of the
  connection's sessions, and the server refuses DML, DDL, `LOCK`,
  `SELECT … FOR UPDATE` and what autonomous transactions write. Before
  23ai the app's check is the only one: `SET TRANSACTION READ ONLY` would
  let functions writing in autonomous transactions, sequences' `NEXTVAL`,
  `LOCK` and DDL through.
- **Transactions**: Oracle has no `BEGIN`: a transaction begins at the
  first change, and the app's Begin sends `SET TRANSACTION READ WRITE`.
  The app reads the open transaction from the server after each
  statement. A schema change commits the open transaction before and
  after it runs, even some that fail. `SET TRANSACTION`, `ALTER SESSION
  SET ISOLATION_LEVEL` and `ALTER SESSION` `ENABLE`, `DISABLE` or `FORCE
  PARALLEL DML` run outside a transaction, and a sequence's `NEXTVAL` is
  not given back by a rollback. Savepoints are not used, as Oracle has no
  `RELEASE SAVEPOINT`: grid edits ask you to end an open transaction
  first. An `UPDATE` counts the rows it matched. Cancelling a statement
  keeps the connection and its transaction. Closing an Oracle connection
  commits its open transaction, so a connection the app gives up with one
  open, as after an Explain's cleanup step failed, is rolled back first;
  should that rollback fail, the error says the changes may have been
  committed.
- **Names**: an unquoted name stands for itself in upper case. The app
  quotes a name it writes unless it is plain and upper case, so a table
  made as `"orders"` is written so.
- **Explain** writes the plan with `EXPLAIN PLAN` into the session's
  `PLAN_TABLE` and reads it; its rows are rolled back, or deleted when a
  transaction was open before. It works on a read-only connection, but
  fails inside a transaction begun with `SET TRANSACTION READ ONLY`.
  **Explain Analyze** runs the statement to its end with the session's
  `STATISTICS_LEVEL` set to `ALL`, reads its plan with what each step took
  from `V$SQL_PLAN_STATISTICS_ALL`, then sets `STATISTICS_LEVEL` to
  `TYPICAL`, Oracle's default. Explaining a write is checked as that
  write, so a read-only connection refuses it. Advice covers columns
  converted to be compared, which keeps their index from being used,
  `MERGE JOIN CARTESIAN`, and full scans that keep few rows.
- **Values**: `NUMBER` shows exactly, every digit; intervals, `JSON` and
  `VECTOR` as Oracle writes them. Table tabs show `XMLType` and `JSON` columns as
  text; a query that selects an `XMLType` column must convert it, with
  `XMLSERIALIZE(CONTENT column AS CLOB)`. See
  [Known limitations](limitations.md) for what the driver cannot read.
- **Table form**: a column that numbers its rows is an identity column
  (`GENERATED BY DEFAULT AS IDENTITY`); comments are set with `COMMENT
  ON`.
- **Users and privileges** lists the users and roles of the pluggable
  database, but Oracle's own. A new user logs in once granted `CONNECT`;
  a grant on every table of a schema needs 23ai. A user that owns objects
  is dropped only with `CASCADE`, which is left to SQL.
- **Activity**: Sessions, Locks and Statistics. **Stop** runs `ALTER
  SYSTEM CANCEL SQL`, which keeps the session and its transaction; **End
  Session** runs `ALTER SYSTEM KILL SESSION … IMMEDIATE`. Statistics are
  those of the statements still in the shared pool.
- **Maintenance**: Gather Statistics (`DBMS_STATS`), Move and Rebuild
  Indexes (`ALTER TABLE … MOVE UPDATE INDEXES`, which locks the table),
  Shrink Space (which needs `ALTER TABLE … ENABLE ROW MOVEMENT` first) and
  Validate Structure.
- **Backup** exports a schema with Data Pump's `expdp` into a dump file in
  a directory object of the **server**, named as `DATA_PUMP_DIR:shop.dmp`
  or `shop.dmp` (in `DATA_PUMP_DIR`), where it stays with its log; a file
  of that name is replaced. **Restore** imports such a file with `impdp`
  into the schema: the dump's objects of that schema only, its tables
  replacing the schema's. See [Known limitations](limitations.md#engines)
  for what Data Pump needs.

What each feature needs on the server, beyond the privileges its
statements need:

| Feature | Needs |
| --- | --- |
| Explain | nothing more: `PLAN_TABLE` is every user's |
| Explain Analyze | `SELECT_CATALOG_ROLE` or `SELECT ANY DICTIONARY` |
| Activity pages | `SELECT_CATALOG_ROLE` or `SELECT ANY DICTIONARY`; Stop and End Session also `ALTER SYSTEM` |
| Users and privileges | `SELECT_CATALOG_ROLE` to list them with their privileges (without it, only the users' names); `CREATE USER`, `ALTER USER`, `DROP USER` or `CREATE ROLE` to change them |
| Backup | `READ` and `WRITE` on the directory object; `DATAPUMP_EXP_FULL_DATABASE` for another user's schema |
| Restore | `READ` and `WRITE` on the directory object; `DATAPUMP_IMP_FULL_DATABASE` for another user's schema |
| Maintenance | nothing more on your own tables; on another user's, `ANALYZE ANY` (statistics, Validate Structure) or `ALTER ANY TABLE` (Move, Shrink Space) |
