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
  - On very large databases, choose for each schema how much of the
    catalog to read: names only, names and columns, or everything. Today
    each object is read when it is first opened.
- **SQL editor:**
  - Vim key bindings, shortcuts that can be changed, several cursors at
    once, and folding of code blocks.
  - Two editors side by side, and more than one window.
  - Mark errors while typing, with suggested fixes.
  - Rename an object everywhere it is used, jump to where an object is
    defined, and list every place it is used.
  - An outline that lists the statements in a file and jumps to them.
  - Build a query by choosing tables and columns instead of typing SQL.
  - Show the catalog queries the app runs by itself. The audit log
    already records statements, grid edits, imports and commands.
- **Query plans:**
  - Draw the plan of a query as a tree or a flame graph instead of a
    table of rows.
  - Read a plan and suggest how to make the query faster.
- **Data grid:**
- **Schema:**
  - Save an ER diagram as PNG or SVG, and change the schema by editing
    the diagram.
  - Data models: build a model from a database, generate DDL from a
    model, and compare two models.
- **Import, export and backup:**
  - Back up and restore databases with `pg_dump`, `mysqldump` and
    `sqlite3`.
  - Copy tables and rows from one database to another, also between
    different engines.
- **Compare and migrate:**
  - Compare the rows of two tables and copy the differences across.
- **Test data and automation:**
  - Fill tables with generated test data.
- **Admin and monitoring:**
- **Redis:**
  - Publish and subscribe to channels.
  - Add entries to streams, and manage consumer groups.
  - Editors for RedisJSON values, vector sets and arrays.
  - Work with Redis Search indexes, and chart TimeSeries keys.
  - Set an expiry time on single hash fields.
  - Delete every key that matches a pattern, and run the commands in a
    file.
  - Analyse memory: the largest keys and namespaces, and how many keys
    expire.
  - Show the slow log, and a live feed of commands (MONITOR).
  - Decode values stored as MessagePack, Protobuf, PHP or Java
    serialization, or Pickle, or compressed with gzip, lz4, zstd, snappy
    or brotli.
  - Choose the character that splits key names into folders. Today it is
    always `:`.
  - Show help for each command, and complete commands, in the console.
  - Import and export keys.
  - Load the rest of a large value. Today only its first part is read.
- **ClickHouse and DuckDB:**
- **Charts:**
  - More kinds of chart.
  - Dashboards built from saved queries, with parameters and automatic
    refresh.
- **AI:**
  - [DEFERRED] Write SQL from a description, explain a query, fix an error, and
    convert SQL from one engine's dialect to another's.
  - [DEFERRED] An MCP server that lets coding agents use a connection, asking
    before each kind of access.
- **Interface:**
  - Custom themes, a choice of font family, translations, and support
    for screen readers and keyboard-only use. Font sizes can already be
    set.
  - A guided tour for new users.

## Known limitations

- **Kerberos:** PostgreSQL only; MySQL's Kerberos plugin has no Go
  client. On macOS, `KRB5CCNAME` must name a `FILE:` cache: the
  system's default cache is not a file.
- **DuckDB:** a read-only and a read-write connection to the same file
  cannot be open at once: the driver keeps one database per file.
- **ClickHouse:** rows are not editable from the grid, because ClickHouse
  changes rows with asynchronous mutations. Use SQL.
- **Large results:** results keep at most 200,000 rows (`db.MaxRows`).
  Export runs the query again and writes page by page, up to the limit
  it is given.
