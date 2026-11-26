# Roadmap

Planned work for DGopher, grouped by area and not in priority order. For
what does not work today and why, see [Known limitations](limitations.md).

## Distribution

- Installers signed for macOS (Developer ID and notarization) and Windows,
  so Gatekeeper and SmartScreen stop warning.
- Install and update through package managers: Homebrew on macOS, winget
  on Windows, and Flatpak and the AUR on Linux.

## Interface

- A Redis editor: a file of Redis commands in an editor tab, as SQL has,
  each command run from it with its reply below, beside the console.
- Applets: customizable, block-based apps for database operations.
- Plugins: an extension API, designed together with applets, whose
  statements go through the same safety checks as the app's own.
- A pending transactions view: every open transaction of every tab and
  connection in one list, with commit and rollback.
- A local history of each query file: every version as it was typed,
  saved or not, to compare with and bring back.
- Git in the navigator: query files marked as changed or new since the last
  commit, with their diff and a command to bring back the committed text.
- A system notification when a statement that ran for long ends while the
  window is not in front.
- `dgopher://` links that open a query file at a line on its connection,
  to paste in a chat or a pull request.
- Translations. The app is in English only for now.

## Connections and engines

- Import saved connections from more database clients: Navicat, DataGrip,
  TablePlus and pgAdmin, as from DBeaver.
- Filters in the navigator: databases, schemas and tables hidden by name
  pattern, per connection, as DBeaver's filters, which its import then
  carries over.
- Bootstrap statements run on each new session, such as `SET search_path`
  or `ALTER SESSION SET`, reviewed as any other statement is.
- Keep-alive: idle sessions pinged so that firewalls and proxies do not
  drop them.
- Group connections by tags, beside folders.
- A PostgreSQL `LISTEN`/`NOTIFY` panel, like the Redis Pub/Sub panel.
- SSH hosts read from `~/.ssh/config`, so a tunnel can name a host set up
  there instead of entering it again.
- MongoDB.
- Valkey by name, the open-source fork of Redis, tested as Redis is.
- Redis `MONITOR`, opt-in and time-limited, to watch the commands a server
  runs. The [safety rules](safety.md) refuse it today, as it slows the
  server; allowing it is a decision of its own.
- libSQL (Turso) and Cloudflare D1, SQLite over the network.
- Cloud data warehouses: Snowflake, BigQuery and MotherDuck.
- Elasticsearch and OpenSearch.

## Data

- Run a statement on several connections, such as staging and production
  or every tenant's schema, with the results side by side or in one grid
  with a column naming each connection.
- Copy related rows: a row and every row its foreign keys reach, such as a
  customer with its orders and their items, to another database.
- What changed since the last run: running a query again marks the rows
  added, removed or changed since the time before.
- Scheduled tasks: an export, a backup, a table copy or a SQL file run on a
  timer, recorded in the audit log.
- Masking on copy and export: fake or scrambled values for sensitive
  columns, so production rows can go to staging or a file safely.
- Spatial values drawn on a map: PostGIS, MySQL and SQL Server geometry
  columns.
- Search for a value across every table of a schema, such as which table
  holds an email address or an ID.
- Open a dropped CSV, Parquet or JSON file as a table tab, with paging,
  Count and its columns' types, instead of an editor and a new query file;
  **Open in Editor** gives today's SQL.
- Query and import more JSON Lines files with DuckDB: those named `.ldjson`
  or `.jsonlines`, and gzip or zstd copies of CSV and JSON files, such as
  `events.jsonl.gz`.

## Administration

- A blocking tree in Server Activity: which session waits for which, with
  the session at the root to cancel.
- An index health report: unused, duplicate and bloated indexes and
  tables of PostgreSQL and MySQL, each with the statement that fixes it.
- Space by table: the sizes of a schema's tables and indexes, largest
  first, or as a treemap.
- Compare the settings of two servers, as `pg_settings` or MySQL's
  variables, listing the differences.
- Server jobs: PostgreSQL's pg_cron, SQL Server Agent jobs, and MySQL
  events edited, run and their history read.
- Live charts of a server's connections, queries per second and cache hit
  ratio beside Server Activity, as ClickHouse's Metrics page has.
- PostgreSQL logical replication: publications, subscriptions and
  replication slots, with how far each slot lags.

## Query plans

- Compare query plans: show two Explain results side by side, such as
  before and after adding an index or rewriting the query, beside the
  existing advice.
- Index suggestions: where the plan advice finds a full scan that keeps few
  rows, offer the `CREATE INDEX` that would help.

## Schema and migrations

- Compare the schemas of two databases, such as staging and production,
  and write the statements that make one match the other.
- Run a procedure or function with a form for its parameters. Today its
  definition opens in an editor.
- Export an ER diagram as Mermaid or DBML text, which GitHub and
  documentation sites draw as a diagram.
- Integration with [dbmate](https://github.com/amacneil/dbmate), the
  database migration tool.

## AI

- Convert SQL from one engine's dialect to another's, as a command. The AI
  chat already writes, explains and fixes SQL.
- An MCP server that lets coding agents use a connection, asking before
  each kind of access, with read-only, read-write and full modes and a list
  of the connections it may use.
- A command line that runs statements on a project's connections, as
  `dgopher query <connection> "<sql>" --json`, through the same safety
  checks and audit log, for scripts, CI and coding agents.
