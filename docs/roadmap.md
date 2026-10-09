# Roadmap and known limitations

## Roadmap

- **Next:**
  - Make the interface more consistent and easier to learn, quick to
    work in, and beautiful.
  - Tighten safety and security so that no common slip can damage data.
  - [DEFERRED] Signed installers, and updates installed by the app through MyGo's
    updater.
  - [DEFERRED] Notebook interface.
  - Keyboard first.

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
  - Several cursors at once, and folding of code blocks.
  - More than one window.
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
  - Translations, and support for screen readers and keyboard-only
    use.
  - A guided tour for new users.

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
