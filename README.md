# DGopher

**Dig into your databases.**

A fast, native database client for **PostgreSQL, MySQL, ClickHouse, SQLite,
DuckDB and Redis**. It is a single Go binary, and its interface is drawn by
[MyGo](https://mygo.egoist.dev)'s native UI, with no webview, no JVM and no
Electron. It opens instantly and is careful by default with the databases
that matter.

## Features

- **One native binary:** no JVM or Electron, and every driver compiled in.
- **SQL editor:** highlighting, completion, and a session per editor.
- **Results grid:** streams large results, filters and sorts, edits rows
  inline, and exports to CSV, JSON, SQL, Parquet and more.
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
  - TLS with a custom CA, and SSH tunnels with host keys checked.
  - Secrets are redacted from history and logs.
- **Audit log:** every statement, edit, import, export and confirmation is
  recorded per project in a tamper-evident hash chain. You can verify it
  and export it.
- **Git-friendly projects:** connections and queries live in your
  repository, without secrets.
- **Redis:** a key browser, value editors and a console.
- **More:** charts, ER diagrams, server activity, CSV import and query
  history.

See [Features](docs/features.md) for the full list, and the [Safety model](docs/safety.md)
and [Audit log](docs/audit-log.md) for the details.

## Quick start

You need Go and a C compiler; see [Development](docs/development.md) for details.

```sh
CGO_ENABLED=1 go run .
```

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
