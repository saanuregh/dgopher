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

| | PostgreSQL | MySQL | ClickHouse | SQLite | DuckDB | Redis |
| --- | --- | --- | --- | --- | --- | --- |
| TLS (prefer, require, verify-full) | ✓ | ✓ | ✓ | — | — | ✓ |
| SSH tunnel | ✓ | ✓ | ✓ | — | — | ✓ |
| SOCKS5 or HTTP proxy | ✓ | ✓ | ✓ | — | — | ✓ |
| Cloud IAM (AWS, Google Cloud, Entra ID) | ✓ | ✓ | — | — | — | — |
| Kerberos | ✓ | — | — | — | — | — |
| Clear-text password (LDAP, PAM) | — | ✓ | — | — | — | — |
| Several databases on one connection | ✓ | ✓ (as schemas) | ✓ (as schemas) | — | — | — |
| In memory | — | — | — | ✓ | ✓ | — |
| Cluster or Sentinel | — | — | — | — | — | ✓ |
| Idle connections disconnect | ✓ | ✓ | ✓ | — | — | ✓ |

## Safety and transactions

| | PostgreSQL | MySQL | ClickHouse | SQLite | DuckDB | Redis |
| --- | --- | --- | --- | --- | --- | --- |
| Transactions and manual commit | ✓ | ✓ | — | ✓ | ✓ | — |
| Savepoints | ✓ | ✓ | — | ✓ | — | — |
| Schema changes inside a transaction | ✓ | — | — | ✓ | ✓ | — |
| Read-only enforced by the server | ✓ | ✓ | ✓ | ✓ | ✓ | — (the app's check only) |
| Large changes held for confirmation | ✓ | ✓ | — | ✓ (files, not in memory) | — | — |
| Idle transactions roll back | ✓ | ✓ | — | ✓ | ✓ | — |
| One connection shared by all tabs | — | — | — | in memory | ✓ | — |

DuckDB and in-memory SQLite share one connection, so all their tabs share
one transaction; see [Safety model](safety.md#duckdb-and-in-memory-sqlite).

## Querying

| | PostgreSQL | MySQL | ClickHouse | SQLite | DuckDB |
| --- | --- | --- | --- | --- | --- |
| Parameters | `:name` | `:name` | `:name`, `{name:Type}` | `:name`, `@name`, `$name` | `:name` |
| Explain | ✓ | ✓ | ✓ | ✓ | ✓ |
| Explain Analyze | ✓ | ✓ | — | — | ✓ |
| Plan advice | ✓ | ✓ | ✓ | ✓ | — |
| Schema switch in the editor | `SET search_path` | `USE` | `USE` | — | `USE` |
| Edit rows in the grid | ✓ | ✓ | — | ✓ | ✓ |

`${var}` variables work on every SQL engine.

## Schema

| | PostgreSQL | MySQL | ClickHouse | SQLite | DuckDB |
| --- | --- | --- | --- | --- | --- |
| Create tables in the table form | ✓ | ✓ | ✓ | ✓ | ✓ |
| Change tables in the table form | ✓ | ✓ | columns only | ✓ (rebuilt when needed) | columns only |
| ER diagram | ✓ | ✓ | view only (no foreign keys) | ✓ | ✓ |
| Data models and DDL | ✓ | ✓ | ✓ | ✓ | ✓ |
| Partitions | ✓ | — | — | — | — |
| Other objects | functions, procedures, triggers, sequences, types, extensions | functions, procedures, triggers, events | projections | triggers | macros, sequences, types |

## Data

| | PostgreSQL | MySQL | ClickHouse | SQLite | DuckDB |
| --- | --- | --- | --- | --- | --- |
| Import files | ✓ | ✓ | ✓ (not in one transaction) | ✓ | ✓ |
| Export | ✓ | ✓ | ✓ | ✓ | ✓ |
| Copy tables, compare rows, test data | ✓ | ✓ | ✓ | ✓ | ✓ |
| Backup | `pg_dump` (archive or SQL) | `mysqldump` (SQL) | — | a copy (`VACUUM INTO`) | Parquet folder (`EXPORT DATABASE`) |
| Restore | ✓ | ✓ | — | ✓ | ✓ |

Redis has its own key [export and import](features.md#keys).

## Administration

| | PostgreSQL | MySQL | ClickHouse | SQLite | DuckDB | Redis |
| --- | --- | --- | --- | --- | --- | --- |
| Sessions, with cancel | ✓ | ✓ | ✓ | — | — | clients |
| End a session | ✓ | ✓ | — | — | — | disconnect a client |
| Locks | ✓ | ✓ | — | — | — | — |
| Slowest statements | ✓ (`pg_stat_statements`) | ✓ (`performance_schema`) | ✓ (`system.query_log`) | — | — | slow log |
| Live metrics | — | — | ✓ | — | — | ✓ |
| Users and privileges | ✓ | ✓ | ✓ | — | — | — |
| Maintenance | VACUUM, VACUUM ANALYZE, VACUUM FULL, ANALYZE, REINDEX | ANALYZE, OPTIMIZE, CHECK | OPTIMIZE, OPTIMIZE FINAL | ANALYZE, REINDEX | — | — |

The slowest-statements page needs the server to collect them: the
`pg_stat_statements` extension on PostgreSQL, `performance_schema` on
MySQL, and query logging on ClickHouse. The page says how to turn each on.

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

