# DGopher

**Dig into your databases.**

A fast, native database client for **PostgreSQL, MySQL, ClickHouse, SQLite,
DuckDB and Redis**. It is a single Go binary, and its interface is drawn by
[MyGo](https://mygo.egoist.dev)'s native UI, with no webview, no JVM and no
Electron. It opens instantly, looks the same on Linux, macOS and Windows,
and is careful by default with the databases that matter.

![DGopher's main window: a project's connections, query files, dashboard
and data model in the navigator, and an editor whose query's result shows
below it](docs/images/main.png)

## Features

- **One native binary:** every driver compiled in, and nothing to install
  beside it.
- **SQL editor:** highlighting, completion from the catalog, a ▶ on every
  statement, folding, multiple cursors, Vim keys, find and replace, Go to
  Definition, problems underlined as you type, and a session per editor.
- **Results grid:** streams large results, filters (in SQL or with a
  builder), sorts, edits rows inline, compares, profiles, and exports to
  CSV, JSON, SQL, Parquet, Excel and more.
- **Safe with production:**
  - Each connection has an environment, whose colour is shown everywhere.
  - On production, writes ask first, destructive statements need the
    connection's name typed, and manual commit is the default.
  - Read-only connections are enforced in the app and on the server.
  - An open transaction is never closed silently, and one left idle rolls
    back.
  - Grid edits are reviewed as SQL and applied in one transaction, one row
    per statement by primary key.
- **Secure:**
  - Passwords stay in the system keychain, an environment variable or a
    command such as a password manager, or are asked every time. Project
    files hold none.
  - TLS with a custom CA, SSH tunnels with host keys checked, and Kerberos
    for PostgreSQL.
  - Secrets are redacted from history and logs.
- **Audit log:** every statement, edit, import, export and confirmation is
  recorded per project in a tamper-evident hash chain. You can verify it
  and export it.
- **Git-friendly projects:** connections, query files, dashboards and data
  models live in your repository, without secrets.
- **Dashboards and charts:** any statement becomes a panel, as a number, a
  chart or a table, with parameters and refresh.
- **Schema:** ER diagrams, a table designer, data models that generate
  migrations, and search across objects and definitions.
- **Data:** copy tables between databases and engines, compare rows,
  generate test data, import CSV, JSON, Parquet, Excel and XML, back up and
  restore.
- **Redis:** a key browser, value editors for every type and module, and a
  console.
- **Keyboard first:** a command palette, quick open for tables and query
  files, and every shortcut customizable.

See [Features](docs/features.md) for the full list, and the
[Safety model](docs/safety.md) and [Audit log](docs/audit-log.md) for the
details.

| | |
| --- | --- |
| ![A dashboard of the sample shop: revenue, orders, customers and products, revenue by month, customers by country, and best sellers](docs/images/dashboard.png) | ![The ER diagram of the sample shop's tables and their foreign keys](docs/images/er-diagram.png) |

## Install

Download the build for your system from the
[latest release](https://github.com/saanuregh/dgopher/releases/latest):
Linux (amd64, arm64), macOS (Apple silicon, Intel) and Windows (amd64).

To build it yourself you need Go and a C compiler; see
[Development](docs/development.md).

```sh
CGO_ENABLED=1 go run .
```

New here? **Try the sample** on the start page opens a small shop database
in SQLite, and the tour shows the window's parts in a minute.

## Documentation

- [Features](docs/features.md)
- [Safety model](docs/safety.md)
- [How SQL is read](docs/sql.md)
- [Audit log](docs/audit-log.md)
- [Where things are kept](docs/storage.md)
- [Development](docs/development.md)
- [Roadmap and known limitations](docs/roadmap.md)

## License

Copyright 2026 DGopher contributors. Licensed under the
[Apache License, Version 2.0](LICENSE).

The icon is based on the Go gopher, designed by Renée French and licensed
under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/).
