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

