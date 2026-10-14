# Engine support

What each engine supports in DGopher. MariaDB connects as MySQL (a
`mariadb://` URL is read as MySQL). [Features](features.md) describes each
feature, and [Known limitations](limitations.md) explains the gaps.

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
