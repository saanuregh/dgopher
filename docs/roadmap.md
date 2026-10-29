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
- Translations. The app is in English only for now.

## Connections and engines

- Import saved connections from other database clients.
- Group connections in sub-folders or by tags.
- A PostgreSQL `LISTEN`/`NOTIFY` panel, like the Redis Pub/Sub panel.
- SSH hosts read from `~/.ssh/config`, so a tunnel can name a host set up
  there instead of entering it again.
- MongoDB.
- Valkey by name, the open-source fork of Redis, tested as Redis is.
- Cloud data warehouses: Snowflake, BigQuery and MotherDuck.
- Elasticsearch and OpenSearch.

## Data

- Scheduled tasks: an export, a backup, a table copy or a SQL file run on a
  timer, recorded in the audit log.
- Masking on copy and export: fake or scrambled values for sensitive
  columns, so production rows can go to staging or a file safely.
- Search for a value across every table of a schema, such as which table
  holds an email address or an ID.
- Open a dropped CSV, Parquet or JSON file as a table tab, with paging,
  Count and its columns' types, instead of an editor and a new query file;
  **Open in Editor** gives today's SQL.
- Query and import more JSON Lines files with DuckDB: those named `.ldjson`
  or `.jsonlines`, and gzip or zstd copies of CSV and JSON files, such as
  `events.jsonl.gz`.

## Administration

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
- Export an ER diagram as Mermaid or DBML text, which GitHub and
  documentation sites draw as a diagram.
- Integration with [dbmate](https://github.com/amacneil/dbmate), the
  database migration tool.

## AI

- Convert SQL from one engine's dialect to another's, as a command. The AI
  chat already writes, explains and fixes SQL.
- An MCP server that lets coding agents use a connection, asking before
  each kind of access.
