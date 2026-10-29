# Project files

A project's shared files are plain JSON and SQL, meant to be committed and
reviewed in pull requests. This page describes each one, so you can read a
diff or edit a file by hand. [Where things are kept](storage.md) lists
every file, including those that are not shared.

- [`dgopher.json`](#dgopherjson)
- [Connections](#connections)
- [Query files](#query-files)
- [Dashboards](#dashboards)
- [Data models](#data-models)
- [Editing by hand](#editing-by-hand)

## `dgopher.json`

The project's root file. A new project starts as:

```json
{"queries":"queries","connections":[]}
```

| Field | Type | What |
| --- | --- | --- |
| `queries` | string | the folder of query files, relative to the project; `queries` by default, and then omitted. It must stay inside the project. |
| `connections` | array | the project's [connections](#connections) |
| `snippets` | array | the project's snippets: `name`, `sql`, and an optional `keyword` that inserts it when typed in an editor |
| `virtualKeys` | object | key columns for tables without a primary key, by table |
| `hiddenValues` | object | columns whose values the grid hides, by table |

`virtualKeys` and `hiddenValues` are keyed by `<connection-id>/<schema>.<table>`.

### Example

A read-only PostgreSQL production connection whose password is in the
system keychain, and one snippet:

```json
{
  "connections": [
    {
      "id": "orders-prod",
      "name": "Orders (prod)",
      "engine": "postgres",
      "host": "db.example.com",
      "port": 5432,
      "user": "app_reader",
      "database": "orders",
      "tls": "verify-full",
      "environment": "production",
      "readOnly": true,
      "ssh": {
        "enabled": false,
        "host": "",
        "user": ""
      }
    }
  ],
  "snippets": [
    {
      "name": "Recent orders",
      "sql": "SELECT * FROM orders ORDER BY created_at DESC LIMIT 100",
      "keyword": "recent"
    }
  ]
}
```

### How the app writes it

- Indented with two spaces, with a final newline.
- Connections sorted by ID, snippets by name (ignoring case), and object
  keys sorted, so a change shows as a small diff.
- Written atomically: to a temporary file in the same folder, then renamed.
- Never over a file that changed on disk since the app read it, as after a
  `git pull`. The save is refused and asks you to reload the project.

## Connections

Each entry of `connections`. Fields that are empty or false are omitted,
except `id`, `name`, `engine`, `host`, `port`, `user`, `environment` and
`ssh`.

### Identity and server

| Field | Values |
| --- | --- |
| `id` | the connection's ID (below) |
| `name` | the name shown; required |
| `engine` | `postgres`, `mysql`, `clickhouse`, `redis`, `sqlite` or `duckdb` |
| `host`, `port`, `user` | the server; `host` is required except for SQLite and DuckDB |
| `database` | the database name. For SQLite and DuckDB, the file path, stored relative to the project when the file is inside it. For Redis, the database number (only 0 in a cluster). |
| `environment` | `development`, `staging` or `production` |
| `color` | a hex color that replaces the environment's color in the navigator and tabs |

The app reads `environment` generously: empty, `dev` or `local` mean
development; `stage`, `test`, `testing`, `qa` or `uat` mean staging. **Any
other value, including a misspelling, means production**, so a typo never
makes a connection less safe.

### Connection IDs

- An ID is lower-case letters, digits and dashes. The app makes it from the
  name: lower-cased, with each run of other characters turned into a dash
  (`Orders (prod)` becomes `orders-prod`). When another connection in the
  project has that ID, it adds `-2`, `-3` and so on.
- A connection without an `id` in the file gets one made from its name when
  the project loads.
- Query files, dashboards and data models refer to connections by ID.
  Change an ID in the app, not by hand, so those references, the saved
  password and the trust move with it (see
  [Connection IDs](features/getting-started.md#connection-ids)).

### Password

At most one of these may be set. With none, the password is kept in the
system keychain.

| Field | Password comes from |
| --- | --- |
| `passwordEnv` | an environment variable; its name must start with `DGOPHER_` |
| `passwordCommand` | a command that prints it; the app shows the command and asks before it first runs |
| `askPassword` | `true`: asked at every connect |
| `identity` | a cloud token: `aws-rds-iam`, `gcp-cloud-sql-iam` or `azure-entra-id`. PostgreSQL and MySQL only, with `tls` set to `verify-full`. `identityRegion` and `identityProfile` set the AWS region and profile. |

`clearTextPassword: true` lets MySQL send the password as clear text, as
LDAP and PAM logins need, and requires `tls` set to `verify-full`.

No password, passphrase or proxy password is ever written to the file.

### TLS, SSH and proxy

| Field | Values |
| --- | --- |
| `tls` | `disable`, `prefer`, `require` or `verify-full` |
| `caFile` | the CA certificate (PEM) to verify the server with |
| `certFile`, `keyFile` | a client certificate and its key (PEM): both or neither, and only with TLS on |
| `ssh` | an object: `enabled`, `host` and `user`; optional `port`, `keyPath`, `useAgent`, `passwordCommand`, and `jump`, a comma-separated list of `[user@]host[:port]` jump hosts. Ignored for SQLite and DuckDB. |
| `proxy` | an object: `kind` (`socks5` or `http`), `host`, `port`, and optional `user`; `host` and `port` are required with a `kind` |

### Behavior

| Field | Values |
| --- | --- |
| `readOnly` | `true` refuses writes, in the app and on the server |
| `commit` | `auto` or `manual`; omitted means manual on production and auto elsewhere |
| `statementTimeout` | seconds; 0 or omitted for none |
| `idleTxTimeout` | seconds before an idle transaction rolls back; 0 or omitted for the environment's default, -1 for never |
| `idleConnectionTimeout` | seconds before an unused connection disconnects; same rules |
| `autoConnect` | `true` connects when DGopher starts |
| `catalog` | how much of a schema to read, by `schema` or `database/schema`: `names`, `columns` or `everything`; omitted means as needed (see [Read Catalog](features/connections.md#read-catalog)) |

### Redis

| Field | Values |
| --- | --- |
| `redis` | an object: `mode` (omitted for one server, `cluster` or `sentinel`), `nodes` (comma-separated `host:port`), `master` (required for Sentinel), `sentinelUser` |
| `keySeparator` | the separator that groups keys into folders; `:` when omitted |

### Trust

A connection that arrives in `dgopher.json` from someone else asks before
it first connects, and again when a pull changes where it connects or how
safely. See [Connections from a project file](safety.md#connections-from-a-project-file).

## Query files

- Query files are the `.sql` files in the queries folder and its
  subfolders. Folders whose names start with a dot are skipped.
- A `-- connection: <id>` line in the first five lines binds the file to a
  connection. The first such line wins.
- A file without one asks, when opened, which connection to use and offers
  to add the line.
- New editors are saved as `query-1.sql`, `query-2.sql`, and so on, starting
  with the connection line and a blank line.

```sql
-- connection: orders-prod

SELECT count(*) FROM orders;
```

## Dashboards

One JSON file per dashboard in `dashboards/`, named after the dashboard
(`Orders` becomes `orders.json`).

| Field | Values |
| --- | --- |
| `name` | the dashboard's name; required |
| `refreshSeconds` | `0` (or omitted) for by hand, or `60`, `300`, `900` or `3600` |
| `panels` | the panels, in order |

Each panel:

| Field | Values |
| --- | --- |
| `title` | the panel's title |
| `connection` | the connection's ID |
| `database` | optional; another database of the connection (PostgreSQL) |
| `sql` | the panel's statement: one read; required |
| `view` | `chart`, `table` or `value` |
| `chart` | for a chart: `kind` (`Bar`, `Stacked Bar`, `Line`, `Area`, `Stacked Area`, `Scatter`, `Pie` or `Histogram`), and optional `x` (the column along the x axis) and `series` (the columns to plot). An unknown kind or column falls back to the automatic choice. |
| `width` | 1 to 4 columns of the dashboard's grid |

Parameters are written in the panels' SQL, such as `:day`. Their values are
each user's own and are not in the file.

```json
{
  "name": "Orders",
  "refreshSeconds": 300,
  "panels": [
    {
      "title": "Orders per day",
      "connection": "orders-prod",
      "sql": "SELECT created_at::date AS day, count(*) FROM orders GROUP BY 1 ORDER BY 1",
      "view": "chart",
      "chart": {"kind": "Line", "x": "day"},
      "width": 4
    }
  ]
}
```

## Data models

One JSON file per model in `models/`, named after the model.

| Field | Values |
| --- | --- |
| `name` | the model's name; required |
| `engine` | the SQL engine its types are written for (any but `redis`) |
| `source` | optional; where the model was read from: `connection`, `schema`, and optional `database` |
| `tables` | the tables, sorted by schema and name when saved |

Each table has `name` and `columns`, and optionally `schema`, `comment`,
`primaryKeyName`, `indexes`, `foreignKeys` and `checks`. Each column has
`name` and `type`, and optionally `nullable`, `default`, `primaryKey`,
`autoIncrement`, `comment`, `extra` and `generated`. A model may not list
the same table twice, and each table needs at least one column.

```json
{
  "name": "Orders",
  "engine": "postgres",
  "tables": [
    {
      "schema": "public",
      "name": "orders",
      "columns": [
        {"name": "id", "type": "bigint", "primaryKey": true},
        {"name": "created_at", "type": "timestamptz"}
      ]
    }
  ]
}
```

## Editing by hand

- Reload the project from its menu after editing `dgopher.json`, so the app
  reads it again. Dashboards and models refuse to save over a file that
  changed on disk, and say so.
- A file that is not valid JSON, or fails its checks, is reported with the
  file's name and the reason. For `dgopher.json`, the project stays in the
  sidebar with the error and no connections until you fix and reload it.
- The files have no version field. The app ignores fields it does not
  know, and drops them the next time it saves that file.
