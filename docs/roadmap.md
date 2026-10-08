# Roadmap and known limitations

## Roadmap

- **Next:**
  - Replace text in the editor. Find already works.
  - Connect to Redis Cluster and Redis Sentinel.
  - Export results as an Excel file.
  - When a query file's `-- connection:` line names a connection that
    does not exist, offer to open the file so the line can be fixed,
    and complete connection ids while typing it.
  - Always run a query file on the connection named in its
    `-- connection:` line, never on another one.
  - Use the built-in DuckDB for more work where it is faster than the
    current code.
  - Create a new, empty DuckDB file from the connection form, as can be
    done for SQLite.
  - Make exports safe, with a row limit that can be configured. Today an
    export either runs a read-only query again or writes only the rows
    already fetched.
  - Write every export format through DuckDB. Parquet and DuckDB files
    already are.
  - Reorganise the new connection form so the common fields come first
    and the rest are easy to find.
  - Make the interface more consistent and easier to learn, quick to
    work in, and beautiful.
  - Lay out ER diagrams so that tables never overlap.
  - Tighten safety and security so that no common slip can damage data.
  - Use `mygo`'s system notification for useful purposes like long running task.
  - [DEFERRED] Signed installers, and updates installed by the app through MyGo's
    updater.
  - [DEFERRED] Notebook interface.
  - Keyboard first.

## Backlog

Features other database clients have that DGopher lacks, from a
comparison in October 2026. Not ordered by priority; items already in
Next or Later are not repeated.

- **Connections:**
  - Reach a database through one or more intermediate SSH servers
    (jump hosts).
  - Connect through an HTTP or SOCKS proxy.
  - Log in with a TLS client certificate.
  - Log in with Kerberos, LDAP or PAM.
  - Log in with cloud identities: AWS IAM, Azure Entra ID, single
    sign-on and OAuth.
  - [DEFERRED] List the databases in an AWS, GCP or Azure account and
    add them as connections.
  - [DEFERRED] Import saved connections from other database clients.
  - [DEFERRED] Group connections in sub-folders or by tags.
  - Choose a connection's colour in the connection form. Today it can
    only be set by editing `dgopher.json`.
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
  - Complete function names and snippets while typing.
  - Snippets with fields to fill in, inserted by typing a short keyword.
  - An outline that lists the statements in a file and jumps to them.
  - Change the editor's database or schema from a list in the editor.
  - Build a query by choosing tables and columns instead of typing SQL.
  - Show the catalog queries the app runs by itself. The audit log
    already records statements, grid edits, imports and commands.
- **Query plans:**
  - Draw the plan of a query as a tree or a flame graph instead of a
    table of rows.
  - Read a plan and suggest how to make the query faster.
- **Data grid:**
  - An editor suited to each type: a tree for JSON, a list for enums, a
    calendar for dates.
  - When editing a foreign key, pick the value from the rows of the
    referenced table.
  - Build a filter by choosing a column, an operator and a value,
    without writing SQL.
  - Compare two results side by side and highlight the differences.
  - Show a histogram of a column, and statistics for every column at
    once. The Calc panel already shows statistics for one column.
  - Hide sensitive values on screen.
- **Schema:**
  - Create and change tables in a form, covering columns, indexes, keys
    and constraints, and show the SQL before running it.
  - Show triggers, functions, procedures, sequences, types, extensions
    and events in the navigator.
  - Show and manage PostgreSQL table partitions.
  - Rename a table or column from the navigator.
  - Open functions and procedures by name, and search the names and
    contents of every object in a database.
  - Save an ER diagram as PNG or SVG, and change the schema by editing
    the diagram.
  - Data models: build a model from a database, generate DDL from a
    model, and compare two models.
  - Generate documentation of a schema.
  - Generate the DDL of many objects at once.
- **Import, export and backup:**
  - Export several tables in one go.
  - Import CSV, JSON, JSON Lines, Excel, XML and Parquet files into a table.
    JSON, JSON Lines and Parquet files dropped on the window can already
    be queried in DuckDB.
  - Create the table while importing a file, instead of needing it to
    exist.
  - Run a large SQL dump file without opening it in an editor.
  - Back up and restore databases with `pg_dump`, `mysqldump` and
    `sqlite3`.
  - Copy tables and rows from one database to another, also between
    different engines.
- **Compare and migrate:**
  - Compare the rows of two tables and copy the differences across.
- **Test data and automation:**
  - Fill tables with generated test data.
- **Admin and monitoring:**
  - Show which sessions hold or wait for locks.
  - Manage database users and their privileges.
  - Show slow queries and statistics of past queries.
  - Run maintenance commands from a menu: VACUUM, ANALYZE, OPTIMIZE and
    REINDEX.
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
  - Show statistics of past ClickHouse queries from `system.query_log`,
    and a dashboard of server metrics.
  - Show ClickHouse projections in the navigator.
  - Summarise a DuckDB table with a profile of every column.
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

- **DuckDB:** a read-only and a read-write connection to the same file
  cannot be open at once: the driver keeps one database per file.
- **ClickHouse:** rows are not editable from the grid, because ClickHouse
  changes rows with asynchronous mutations. Use SQL.
- **Large results:** results keep at most 200,000 rows (`db.MaxRows`).
  Export has no limit: it runs the query again and writes page by page.
