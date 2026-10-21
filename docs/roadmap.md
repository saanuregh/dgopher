# Roadmap

Planned work for DGopher, grouped by area and not in priority order. For
what does not work today and why, see [Known limitations](limitations.md).

## Distribution

- Installers signed for macOS (Developer ID and notarization) and Windows,
  so Gatekeeper and SmartScreen stop warning.

## Interface

- An editor interface for Redis.
- Applets: customizable, block-based apps for database operations.
- Translations. The app is in English only for now.

## Connections and engines

- Import saved connections from other database clients.
- Group connections in sub-folders or by tags.
- Support for more database engines.
- Features built on ClickHouse's native capabilities.
- A PostgreSQL `LISTEN`/`NOTIFY` panel, like the Redis Pub/Sub panel.

## Query plans

- Compare query plans: show two Explain results side by side, such as
  before and after adding an index or rewriting the query, beside the
  existing advice.
- Index suggestions: where the plan advice finds a full scan that keeps few
  rows, offer the `CREATE INDEX` that would help.

## Schema and migrations

- Improved ER diagrams.
- Integration with [dbmate](https://github.com/amacneil/dbmate), the
  database migration tool.

## AI

- Write SQL from a description, explain a query, fix an error, and convert
  SQL from one engine's dialect to another's.
- An MCP server that lets coding agents use a connection, asking before
  each kind of access.
