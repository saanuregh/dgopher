# Connections

Keys are written as on macOS; on Linux and Windows, ⌘ is Ctrl and ⌥ is Alt.
[Keyboard shortcuts](../keyboard.md) lists the default keys.

## Engines

| Engine | Notes |
| --- | --- |
| PostgreSQL | browse its other databases from one connection |
| CockroachDB, YugabyteDB, TimescaleDB | PostgreSQL's family: connect and work as PostgreSQL does, except where [Engine support](../engines.md#engines-of-a-family) says otherwise |
| MySQL | |
| MariaDB, TiDB | MySQL's family: connect and work as MySQL does, except where [Engine support](../engines.md#engines-of-a-family) says otherwise (TiDB's read-only is the app's check alone) |
| SQL Server | SQL Server 2019 and later, and Azure SQL; databases above schemas; password, Entra ID or Kerberos ([engines](../engines.md#sql-server)) |
| Oracle | Oracle 19c and later; a service name, as a pluggable database's, or a SID; password; TLS with an optional wallet ([engines](../engines.md#oracle)) |
| ClickHouse | native protocol or HTTP, chosen under **Protocol**; the port defaults to 9000 or 8123, 9440 or 8443 with TLS required or verified |
| SQLite | a file, or in memory; **New…** creates an empty file |
| DuckDB | a file, or in memory; **New…** creates an empty file |
| Redis | one server, a cluster, or the master Sentinel names |

**New…** never replaces a file that is already there; it opens it as it
is.

The list also offers **Neon** and **Supabase**, which fill in a
PostgreSQL connection with TLS verify-full, the service's database and,
for Supabase, its user, and show the form of its host names.

After connecting, DGopher reads which engine of its family the server
is. When it differs from the connection's, as a CockroachDB server saved
as PostgreSQL, a notice says so and offers **Use CockroachDB**, which
saves the connection with that engine; nothing changes without it. The
notification keeps the offer, and **Don't Ask Again** stops it for that
server's version. The offer does nothing while a connection form is open,
or once the connection's server or engine changed since; when a saved
password could not be read, the form opens to have it entered before
saving. **Test Connection** names the server's engine too, and offers to
change the form's.

## The connection form

- **General** holds what most connections need: where the database is, who
  connects and where the password comes from, the environment, a color, and
  read-only.
- **Options** holds auto-connect, the commit mode, the idle-transaction and
  idle-connection limits, and the statement timeout.
- **Network** holds TLS, the proxy and SSH. It is marked when SSH, a proxy
  or a client certificate is set.
- **Test Connection** reports the server's version and how long connecting
  took.
- **Auto-connect** (off by default) opens the connection when DGopher
  starts, and connects its editors as they come into view.

## Environments and colors

Each connection has an environment: development, staging or production.
It decides what asks before running; see [Safety model](../safety.md).

- A connection to another machine saves only once you choose its
  environment, Development included, and again when an edit points it at
  another machine. This is because the default, Development, asks nothing
  before a write. Local servers and files keep the default. Connections in
  the project file keep the environment they say.
- A connection's own color replaces its environment's in the navigator, its
  tabs and the band above its work. On staging and production, the band and
  the questions before a write keep the environment's color, so a color
  from a shared project cannot make production look safe.

## Connection URLs

Paste a URL, or a file path, into **From URL** to fill in the form,
including TLS.

- Schemes: `postgres://`, `postgresql://`, `cockroachdb://`,
  `cockroach://`, `yugabytedb://`, `timescaledb://`, `mysql://`,
  `mariadb://`, `tidb://`, `clickhouse://`, `ch://`, `redis://`,
  `rediss://`, `sqlite://`, `sqlite3://`, `duckdb://` and `file://`.
- ClickHouse's JDBC URLs, as DBeaver shows them, choose HTTP:
  `clickhouse:http://`, `clickhouse:https://` (verify-full TLS), with
  `ch` for `clickhouse` and an optional `jdbc:` before. A URL that names
  no protocol keeps the old rule: HTTP on ports 8123 and 8443, native
  elsewhere.
- TLS is read as each driver reads it:
  - PostgreSQL's `sslmode`: `verify-ca` and `verify-full` become
    verify-full.
  - MySQL's `ssl-mode` or `tls`: `tls=true` becomes verify-full.
  - ClickHouse's `secure` or JDBC's `ssl`, and `rediss://`, become
    verify-full, or require (encrypted, not verified) with `skip_verify`.
  - `ssl=true` or `ssl=false` on PostgreSQL and MySQL become require or
    disable.
- An unknown value, two values that disagree, or a `tls_server_name` is
  shown as an error rather than quietly taken as prefer. (The app checks
  the certificate against the URL's host.)

## Passwords

The password comes from the system keychain, a `DGOPHER_*` environment
variable, a command such as a password manager, a cloud identity, or is
asked every time. [Secrets](../safety.md#secrets) explains each.

## TLS

- Modes: off, prefer, require, or verify (the certificate and the host
  name), with a custom CA file.
- A client certificate logs in where the server asks for one.
- Prefer uses TLS when the server offers it, and connects without TLS when
  it does not.
- Prefer and require encrypt but do not check who the server is. So on
  PostgreSQL, under either mode, a server that asks for the password in
  clear text is refused. See [Secrets](../safety.md#rules-for-each-protocol).
- Whether TLS was used shows after the server's version ("TLS" or "no
  TLS"), and in the connect's audit entry.
- ClickHouse over HTTP ignores `HTTP_PROXY` and refuses redirects to
  another host, so its password stays with the server.

## SSH tunnels

- Log in with the SSH agent, a key file with a passphrase, or a password.
  The passphrase or password can come from the keychain or a command.
- Jump hosts work as `ssh -J` does. A jump host logs in with the key file
  or the agent; the password goes only to the final SSH host. So a tunnel
  through jump hosts needs a key or the agent.
- Host keys are checked against `known_hosts`, including each jump host's.
  A new host must be trusted explicitly after you compare its fingerprint.
  A changed host key is refused as a possible attack.
- **A tunnel that ends after connecting,** as when the network drops or
  the SSH server restarts, fails its connection with "SSH tunnel closed"
  and the reason, in a dialog. Open transactions were lost with it: each
  one's rollback is audited and named in the error. Tabs stay open;
  **Reconnect** connects again. The app never reconnects on its own.

## Proxies

SOCKS5 or HTTP CONNECT, to the server or to the SSH host, with a user and
a password kept in the keychain.

## Cloud identities and Kerberos

- **Cloud IAM.** PostgreSQL and MySQL can log in with an AWS IAM, Google
  Cloud IAM or Microsoft Entra ID token instead of a password. The cloud's
  own tool (`aws`, `gcloud`, `az`) prints the token after its single
  sign-on. The app makes a new one for new connections as it ages, and
  sends it only over verified TLS: verify-full, with the server's CA file
  unless the system trusts it (AWS's RDS CA bundle, Cloud SQL's server CA).
  AWS takes a region and a profile.
- **LDAP and PAM.** MySQL can send the password as clear text, over verified
  TLS only, as LDAP and PAM logins need. PostgreSQL's LDAP and PAM logins
  take the password as usual, under require or verify-full.
- **Kerberos.** A PostgreSQL server that asks for it gets the ticket from
  your `kinit`, from the credentials cache that `KRB5CCNAME` names, as
  `krb5.conf` (or `KRB5_CONFIG`) sets up. On Windows it uses your login's
  credentials.

## Redis topologies

- **Cluster.** Connect through any node. The key browser scans every
  master, and the console reaches a key on whichever node holds it.
- **Sentinel.** The connection follows the master to its replacement after
  a failover. It logs in to the sentinels with their own user and password,
  kept in the keychain.
- **SSH.** Through a tunnel, every node is reached through the SSH server.
- The form also sets the key separator for the key tree, and the database
  index.

## The navigator

Each project shows in sections: Connections (open at first), Queries,
Dashboards and Data Models. Each shows a count and, when pointed at, a +
that makes another. With only one project, its name is in the sidebar's
top row and its sections at the top.

- A query file shows, under its name, the connection its
  `-- connection:` line names, in its environment's color, or in the
  warning color if no connection has that ID.
- A connection is a tree of databases, schemas, tables, views and columns,
  read as you open it, with row estimates. A database with one schema shows
  that schema's folders directly.
- The navigator follows the tab in front, selecting its query file, table,
  dashboard, data model or connection, and opening what holds it.
- Resting the pointer on a table or a view, or Alt+clicking it, shows its
  [card](sql-editor.md#hover-cards), beside the sidebar.

### Schema objects

A schema's other objects are in folders by kind. Opening one shows its
definition in an editor, where you can change and run it.

The folders are functions and procedures, triggers, sequences and types,
where the engine has them, plus extensions (PostgreSQL), events (MySQL),
macros (DuckDB) and projections (ClickHouse).

A PostgreSQL partitioned table lists its partitions with their bounds.
Open a partition's rows, detach it, or make a new one, its statement
written for the table's partitioning strategy.

### Read Catalog

For very large schemas, **Read Catalog** on a schema sets how much the app
reads. The choice is kept in the project file.

| Choice | Reads |
| --- | --- |
| As Needed | tables with their sizes; a table's columns once it is opened or named in an editor |
| Names Only | no sizes, and columns only once a table is opened, never for completion or the editor's checks |
| Names and Columns | every column of the schema in one query, so completion needs no more |
| Everything | also routines, triggers, sequences and types |

### Menus

| Item | Menu offers |
| --- | --- |
| Empty sidebar | New Connection…, New Project…, Add Existing Folder… |
| Project | New Connection…, New SQL Editor (with a SQL connection), New Dashboard…, New Data Model…; Query History; Audit Log; Show in Files; Reload; Remove from Sidebar… |
| Connection | Connect or Disconnect (Cancel Connecting while it connects), Refresh, New SQL Editor, Run SQL File…, Search Objects…, Server Activity (servers only), Users and Privileges (where supported), Catalog Queries (SQL only), Back Up…, Restore… (not on read-only connections), a submenu for its schema, Edit Connection…, Duplicate, Delete Connection…; on Redis, Open Key Browser |
| Schema | New Table…, View ER Diagram, Import File as New Table…, Export Tables…, Copy Tables to Another Database…, Generate SQL Script…, Generate Documentation…, Save as Data Model…, Run SQL File…, Back Up…, Restore…, Search Objects…, Read Catalog, New SQL Editor, Refresh |
| Table or view | Open Data, View Structure, View DDL, New SQL: SELECT, Build a Query…, Copy Name, Rename…, Export Data…, Import Data…, Generate Test Data…, Drop…; tables also Copy to Another Database…, Compare Rows with…, Maintenance and Truncate…; partitioned tables also New Partition… |
| Partition | Open Data, Detach… |
| Column | Copy Name, Rename… (columns of tables) |
| Function, sequence, type, … | Open Definition, Copy Name |
| Query file, dashboard, data model | Rename…, Duplicate (query files), Delete… |

Keys in the navigator: ⌘↵ opens a `SELECT *` of the table in a new editor,
⌘R reads the schema again, ⌘C copies the table's name, F2 renames it, F4
shows its structure, and Delete opens a `DROP` of it in a new editor to
review and run. On a query file, dashboard or data model, Delete deletes
it.

**Catalog Queries**, on a connection's menu or in the palette, lists the
latest queries the app ran on its own to read the catalog (for the
navigator, completion and so on), with each one's time, arguments and
error.
