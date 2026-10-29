# Server administration

## Server Activity

For PostgreSQL, MySQL, SQL Server, Oracle, ClickHouse and Redis, from a
connection's menu or the palette. [Engine support](../engines.md#administration)
says what each engine's pages show and the permissions they need.

![The Sessions page of a PostgreSQL server: a statement that has run for a
while, and an idle session](../images/server-activity.png)

| Page | Engines | Shows |
| --- | --- | --- |
| Sessions | PostgreSQL, MySQL, SQL Server, Oracle, ClickHouse | sessions and running queries, refreshed every 2 seconds; cancel a query (all but SQL Server), or end a session (all but ClickHouse) |
| Locks | PostgreSQL, MySQL, SQL Server, Oracle | each lock held or waited for, with the sessions a waiting one waits for; MySQL's metadata locks too |
| Statistics | PostgreSQL, MySQL, SQL Server, Oracle, ClickHouse | the statements that took the most time, from `pg_stat_statements`, MySQL's `performance_schema`, SQL Server's `sys.dm_exec_query_stats`, Oracle's `V$SQLSTATS`, or ClickHouse's `system.query_log` over the last day, with calls, mean and maximum times, and rows (up to 500) |
| Metrics | ClickHouse | queries and merges running, connections, memory, parts and load, and queries and rows per second |
| Redis | Redis | headline metrics and clients; disconnect a client (of one node, in a cluster) |

Cancelling and ending sessions always asks; see
[Safety model](../safety.md#grid-edits).

## Users and Privileges

For PostgreSQL, MySQL and ClickHouse: users and roles, with what each may
do and the roles each belongs to.

- Create users and roles, change a password, grant privileges on a table or
  on every table of a schema, add to a role, revoke a privilege, or drop.
- Each action shows its statements and asks first.
- A password shows nowhere. On PostgreSQL it is hashed (SCRAM-SHA-256)
  before it is sent, as psql's `\password` does.
- Dropping a PostgreSQL role reassigns what it owns to you; it never drops
  those objects.

## Maintenance

From a table's menu:

| Engine | Commands |
| --- | --- |
| PostgreSQL | VACUUM, VACUUM ANALYZE, VACUUM FULL (locks reads and writes), ANALYZE, REINDEX |
| MySQL | ANALYZE, OPTIMIZE, CHECK |
| SQLite | ANALYZE, REINDEX |
| ClickHouse | OPTIMIZE, OPTIMIZE FINAL |

Each runs in an editor, through the safety policy. Manual commit opens no
transaction for a statement that cannot run in one, such as VACUUM.
