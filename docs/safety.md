# Safety model

DGopher is built to be used on production databases. This page describes
what it does to keep you from changing data by accident, how it handles
transactions, and how it keeps passwords safe.

Every statement goes through one policy,
[`internal/safety`](../internal/safety), before it reaches the server. A
statement the policy cannot classify counts as a write.

- [The policy at a glance](#the-policy-at-a-glance)
- [Read-only connections](#read-only-connections)
- [Large changes](#large-changes)
- [Scripts and SQL files](#scripts-and-sql-files)
- [Transactions](#transactions)
- [Lost and idle connections](#lost-and-idle-connections)
- [Grid edits](#grid-edits)
- [How statements are classified](#how-statements-are-classified)
- [What the policy cannot catch](#what-the-policy-cannot-catch)
- [Secrets](#secrets)

## The policy at a glance

Each connection has an environment: development, staging or production.
The environment decides what asks first.

| Statement | Development | Staging | Production |
| --- | --- | --- | --- |
| Reads | run | run | run |
| Writes (`INSERT`, `UPDATE`, …) | run | run | ask |
| Schema changes (`CREATE`, `ALTER`, …) | run | ask | ask |
| Destructive statements (below) | ask | ask | ask, and you type the connection's name |
| Default commit mode | auto | auto | manual |

ClickHouse and Redis always use auto-commit.

![On a production connection, a DELETE without WHERE asks for the
connection's name to be typed before it runs](images/production-confirm.png)

### Destructive statements

These always ask, in every environment:

- `DROP` and `TRUNCATE`.
- Replacing a table, as `CREATE OR REPLACE TABLE`.
- `UPDATE` or `DELETE` without a `WHERE`, or with one that is always true:
  `WHERE 1=1`, `WHERE TRUE`, `id = id`, `1=1 AND 2=2`, MySQL's `&&`, and
  casts such as `TRUE::bool` or `CAST(1 AS bool)`. This includes such a
  statement inside a `WITH`.
- ClickHouse `ALTER`s that lose rows: `CLEAR COLUMN`, `MODIFY TTL`,
  `MATERIALIZE TTL`, `REPLACE PARTITION`, `DETACH PARTITION` and
  `MOVE PARTITION … TO TABLE`.
- `ALTER … TRUNCATE PARTITION`.
- Redis `FLUSHALL`, `FLUSHDB`, `KEYS` and similar commands.

### Redis

Redis commands that write follow the Writes row. Commands that need a
connection of their own, such as `SELECT`, `AUTH`, `MULTI`, `SUBSCRIBE`
and `MONITOR`, are refused everywhere. The console and the key browser
share a pool of connections, so such a command would affect the others.

## Read-only connections

A read-only connection refuses writes in two places:

- **In the app.** A statement the app cannot classify counts as a write
  and is refused.
- **On the server.** The app sets the server's own read-only mode:

  | Engine | Setting |
  | --- | --- |
  | PostgreSQL | `default_transaction_read_only` |
  | MySQL | `transaction_read_only` |
  | MariaDB | `tx_read_only`, which every release from 10.11 has |
  | TiDB | none: TiDB refuses the read-only setting (error 1235), so the app's check is the only one |
  | CockroachDB | `default_transaction_read_only`; the app also refuses `CREATE STATISTICS`, `EXPLAIN ANALYZE (DEBUG)`, any `crdb_internal` function but known reads, a `SET search_path` that names `crdb_internal`, and a `SET` or `RESET` of `allow_unsafe_internals`, which it lets through |
  | YugabyteDB | `default_transaction_read_only`; the app also refuses any `yb_` function but known reads, and a `SET` or `RESET` of `yb_disable_transactional_writes` or `yb_enable_upsert_mode`, which it lets through |
  | ClickHouse | `readonly=2` |
  | SQLite | `mode=ro` with `query_only` |
  | DuckDB | `access_mode=READ_ONLY` |

The app also refuses any statement that could switch read-only off: one
that contains `READ WRITE`, `set_config`, `read_only`, `readonly`,
`query_only`, `access_mode` or `read_write`, even inside a string or a
quoted name. Before it looks, it decodes PostgreSQL `U&"…"`, `U&'…'` and
`E'…'` escapes and ClickHouse backslash escapes, and it refuses any
`UESCAPE`.

This matters because the server's setting is not always enough. A
PostgreSQL session can turn its setting off with `SET`, and DuckDB's
setting does not cover a database attached `READ_WRITE`. The app refuses
both statements, so for these engines the app's check is the one that
holds.

## Large changes

On staging and production, in auto-commit, a statement that may change
many rows runs in a transaction of its own first: `UPDATE`, `DELETE`,
`MERGE`, `REPLACE`, or an upsert (`ON CONFLICT DO UPDATE`,
`ON DUPLICATE KEY UPDATE`, `INSERT OR REPLACE`).

If it changed more rows than the limit, the app asks whether to commit.
The limit is in **Settings → Safety → Large changes** (1,000 rows by
default; 0 turns the check off).

- If you do not agree, the change rolls back.
- A stopped run rolls back. Either way, the script stops.
- Closing the editor while it asks tells you what will roll back.
- In manual commit the transaction stays open anyway, and the editor shows
  its row count.

The count is the one the server reports. An upsert's or a `MERGE`'s count
includes the rows it inserts. MySQL counts the rows matched, changed or
not, and counts a row that `ON DUPLICATE KEY UPDATE` changes twice.

These changes are not held for a count:

- On DuckDB and in-memory SQLite. Their sessions share one connection and
  one transaction, and holding the change would take that transaction from
  the other editors.
- On a MySQL table without transactions, such as MyISAM. A rollback that
  could not undo such a table's changes says so.
- In a MySQL session with autocommit off: the change waits for your
  `COMMIT` anyway.
- `EXPLAIN ANALYZE`, MariaDB's `ANALYZE` of a statement, `RETURNING`, and
  a `WITH` that changes rows. Their rows are read, not counted.
- Run SQL File, imports, copies and generated rows. They ask once for
  everything they will run.

On production, applying grid changes or a row comparison that updates or
deletes more existing rows than the limit asks for the connection's name,
as an `UPDATE` without `WHERE` does.

## Scripts and SQL files

**Scripts in an editor.** On production, a script asks before each write:
Run, Skip, Run All or Cancel. A destructive statement, or one that commits
the open transaction, still asks on its own after Run All.

**Run SQL File** runs a file without opening it in an editor:

1. It reads the whole file first and asks once for all of it. On
   production, a file that writes asks for the connection's name.
2. It names any statement that commits a transaction the file began
   itself, such as MySQL's DDL. Everything that ran before that statement
   stays even if the file fails later.
3. It runs the file in one transaction, all or nothing, when the engine
   supports it and the file begins and ends no transaction of its own.
4. It checks each statement against what you agreed to as it runs.
5. If the file stops with a transaction it began still open, the app rolls
   it back, records the rollback in the audit log, and fails the run with
   the line that began it. On MySQL the app asks the server whether a
   transaction is open, so a dump that turns autocommit off and then
   commits ends well.

## Transactions

### What the app shows

The transaction state shown is the one the server or its driver reports:
MySQL's status flags, SQLite's autocommit flag, DuckDB's transaction ID,
and PostgreSQL's transaction status. It does not depend on how the
statement was written. A comment before `BEGIN`, `ROLLBACK TO SAVEPOINT`,
`COMMIT AND CHAIN` and `XA START` leave a transaction open. A deadlock or
an implicit commit ends it.

The title bar lists every open transaction and how long it has been
open.

MySQL's `SET autocommit = 0` does not open a transaction. The next
statement that reads or writes a table does. Until then the editor's bar
says autocommit is off, and closing or quitting asks nothing. Its writes
then wait for `COMMIT`, as in manual commit: a statement that would commit
them implicitly asks first, and grid edits stay in the transaction they
open.

### Manual commit

In manual commit, every write runs in a transaction. Before a run's first
statement the app begins one if none is open. After a statement that
committed it implicitly, it begins another and says so. It begins none for
a statement that takes effect at once, which Roll Back would not undo (see
below), or that cannot run in a transaction.

### Closing, disconnecting and quitting

- **An open transaction.** Closing a tab, disconnecting, removing a
  project or quitting asks first: Commit, Roll Back, or Cancel. Commit and
  Roll Back then do what you asked, as in Commit and Quit.
- **Work in progress.** Disconnecting, removing a project, deleting a
  connection or quitting while work runs on the connection lists that work
  and what stopping it loses, and asks. This covers Run SQL File, imports,
  copies, generated rows, applying a row comparison, backups and restores,
  exports, schema changes, applying grid changes, and Redis bulk work. If
  you agree, the app stops the work and waits up to 10 seconds for it to
  roll back what it left open.
- **Tabs stay when you disconnect.** Editors keep their text and connect
  again at the next run. Tables, diagrams and the other views of the
  connection wait in place and open again on Connect. The table designer
  closes. Deleting a connection or removing its project closes its tabs.
- **A clean start for the next tab.** Closing a tab closes its connection.
  The next tab inherits no schema, `search_path`, role, variables, locks,
  temporary tables or transaction.

### DuckDB and in-memory SQLite

These engines use a single connection, so all their tabs share one
transaction. That transaction belongs to the tab that began it.

- Other tabs say that they run inside it.
- Their Commit and Roll Back are refused and name the owner tab. So are
  their typed `COMMIT`, `END`, `ROLLBACK` (not `ROLLBACK TO`), `ABORT`,
  `BEGIN` and `START TRANSACTION`. The check runs before a run and again at
  each such statement, in case another tab began a transaction meanwhile.
- The tab's status bar, the idle warning and the close and quit prompts name only
  the owner.
- Work in other tabs counts as use of the owner's transaction, so it does
  not roll back as idle while they work.
- Every tab reads the shared state again each frame, so the owner sees
  when another tab's statement ends or fails the transaction.
- A Commit or Roll Back sends nothing when the open transaction belongs to
  another tab, or when none is open.
- A schema change that runs in its own transaction (the table designer, a
  model's migration) is refused while a transaction is open, because its
  `BEGIN` would fail and abort it. So is a DuckDB backup, whose export
  would include the transaction's uncommitted rows.
- A Commit, or a typed `COMMIT` or `END`, of a transaction that something
  else aborted (a catalog read cut short, a failed backup) rolls it back
  and says that nothing was committed. DuckDB itself would answer with a
  rollback and no error.
- Closing the owner's tab rolls its transaction back as soon as the
  connection is free. No other tab takes the transaction over meanwhile.

### MySQL implicit commits

On MySQL, a statement that commits an open transaction implicitly asks
first, because Roll Back could no longer undo what ran before it. These
are DDL and the rest of
[MySQL's list](https://dev.mysql.com/doc/refman/8.4/en/implicit-commit.html),
except statements on temporary tables. MariaDB adds `CREATE`, `ALTER` and
`DROP SEQUENCE`; TiDB any `CREATE`, `ALTER` or `DROP` but a local
temporary table's or a plan binding's, and `FLASHBACK` and `RECOVER`.

When no transaction is open yet, it still asks if a write comes before it
in the same run inside a transaction: the app's own in manual commit, or
one the run begins with `BEGIN` or `START TRANSACTION`.

### Schema changes that take effect at once

On YugabyteDB, a schema change, `TRUNCATE`, `GRANT` or `REVOKE` inside an
open transaction takes effect at once: it is not part of the transaction,
and does not commit it. Roll Back does not undo it, so it asks first, as
an implicit commit does, and manual commit begins no transaction for it.
The MySQL family's `CREATE TEMPORARY TABLE` is the same: it commits
nothing, and Roll Back keeps the table.

YugabyteDB builds an index without online backfill inside a transaction,
and writes to the table meanwhile can leave it inconsistent. So manual
commit runs `CREATE INDEX` outside one, unless it says `NONCONCURRENTLY`.

### Cancelling and stopping

- **MySQL cancel.** Esc, Stop and the statement timeout send `KILL QUERY`
  from a separate connection outside the pool. This stops the statement
  and keeps the session: its transaction, variables, `USE` and temporary
  tables. If the `KILL` cannot be sent, or the statement has not ended 5
  seconds after it, the app drops the connection instead. The `KILL` names
  the session's connection ID as MySQL numbers it; a proxy that shares
  server connections, such as ProxySQL or RDS Proxy, may number it
  differently.
- **Count.** Stop ends the `COUNT` statement on PostgreSQL, SQLite and
  DuckDB. On MySQL and ClickHouse the app stops waiting and closes the
  connection, and the server may finish the count. On DuckDB, stopping
  would abort the transaction open on its one connection, so Stop is
  disabled and says why.
- **Fetch All.** Stop ends after the page being read. The rows stay, and
  the query stays open to read more.

## Lost and idle connections

### After a lost connection

A connection can be lost to a network drop, `KILL`,
`pg_terminate_backend`, or a MySQL cancel that dropped the connection. The
app reconnects, but the new session has lost its schema, `search_path`,
role, variables and temporary tables. So the next statement is refused
once, with that explanation. Running it again runs it on the new
connection, and the editor shows the schema it is in now.

A script, even one set to continue past errors, and Run SQL File stop at
that point, so the rest never runs without what the lost session had set.
A grid apply that loses its connection leaves the refusal for your next
statement.

An SSH tunnel that ends under a connection is different: nothing can be
reached until it is opened again, so the app does not reconnect. The
connection fails with "SSH tunnel closed", each open transaction's
rollback is audited and named in the error it shows, and the tabs wait for
**Reconnect**.

### Idle transactions

A transaction left idle rolls back. A warning appears 10 seconds before,
and you can cancel it. The limits are:

| Environment | Rolls back after |
| --- | --- |
| Production | 10 minutes |
| Staging | 15 minutes |
| Development | 30 minutes |

### Idle connections

A connection left unused disconnects:

| Environment | Disconnects after |
| --- | --- |
| Production | 1 hour |
| Staging | 2 hours |
| Development | 4 hours |

DuckDB and SQLite stay connected. A connection never disconnects while it
holds something: an open transaction, uncommitted rows, grid changes not
yet applied, a running statement, export, job or dashboard panel, an open
table, diagram, Redis, activity, users or search tab, or an open copy or
row-comparison dialog.

After it disconnects, its editors stay with the rows they show, and its
tree in the sidebar stays open, marked Not connected. Neither reconnects
while it shows. An editor brought back into view reconnects it if the
connection auto-connects, and so does opening it in the sidebar again. The
next run reconnects it and is refused once, as after a lost connection.

Each connection can set its own limits on the Options page of its form:
a number of minutes, 0 for the environment's default, or Never roll back
and Never disconnect.

## Grid edits

![A grid edit before it is applied: the changed cell, and the UPDATE
statement to review in the question that asks to apply it](images/edit-review.png)

- Grid edits are shown as SQL to review, then applied in one transaction.
  Each statement must change exactly one row by primary key, or everything
  rolls back.
- Inside an open transaction, grid changes go behind a savepoint. A failed
  apply rolls back to it and keeps the transaction. DuckDB has no
  savepoints, so grid changes are refused there while a transaction is
  open: a failed change would abort all of it.
- Stop while changes are applied takes effect before the next change. The
  statement already sent runs to its end, then every change rolls back (to
  the savepoint, inside an open transaction, which stays open). Cutting
  the statement short instead would make SQLite roll back the whole
  transaction and might make MySQL drop the connection. A `COMMIT` or
  `RELEASE` once sent is never cut short, so its outcome is always known.
- Deleting rows names the tables whose foreign keys point at them.
- Reading rows again while changes are pending (refresh, sort, filter,
  fetching more, running the editor again) asks first: Apply, Discard or
  Cancel.
- A result whose statement does more than read, such as one that calls
  `nextval` or uses `FOR UPDATE`, cannot be edited, and says why: reading
  it again would run it again.

**Server activity.** Cancel query and End session (and Redis's
`CLIENT KILL`) go through the policy as if the connection were not
read-only. This lets a read-only monitoring connection stop runaway work;
the server's privileges decide whether it may. They always ask, and End
session on production asks for the connection's name.

## How statements are classified

The policy decides from each statement's class: read, write, schema
change, transaction or session, and whether it is destructive. The main
rules are below; [How SQL is read](sql.md#classification) has the full
list.

- **The app splits a script the way the server does,** with each server's
  comments, quotes, escapes and dollar-quoted strings. Tests check these
  rules against the real servers.
- **Ambiguous strings count as their most dangerous reading.** A session,
  role, database or server setting can change where strings end:
  PostgreSQL's `standard_conforming_strings = off` makes `\` an escape,
  MySQL's `NO_BACKSLASH_ESCAPES` makes it a plain character, and MySQL's
  `ANSI_QUOTES` makes `"…"` a name. A statement that holds a `\` (or, on
  MySQL, a `"`) is read under every rule its server can switch to. So a
  write with a backslash in a string may ask where it otherwise would not.
- **More than one statement in one text** counts as a destructive write
  and asks everywhere. On PostgreSQL the server refuses such text too.
- **Code keeps the danger of what it holds.** Creating a procedure,
  function or trigger whose body has a `DROP` asks as the `DROP` would.
- **Hidden writes count as writes:** a `SELECT` that calls a function with
  side effects (such as `pg_terminate_backend`, `nextval` or MySQL's
  `GET_LOCK`), `SELECT … FOR UPDATE` and `FOR SHARE` (which lock rows while
  the result or transaction is open), `SELECT … INTO`, and statements that
  change the server rather than the session (`SET GLOBAL`, `RESET`, …).
- **Secrets are redacted** from history and the audit log, including from
  error messages that quote the statement.

## What the policy cannot catch

The policy reads text; it cannot know everything a statement will do.

- SQL built at run time, such as `EXECUTE format(…)`, cannot be known.
- A function that writes but is not on the app's list looks like a read.

For a guarantee that nothing can bypass, connect as a database role that
has only read privileges.

DuckDB also reads local files and, by default, installs extensions from the
network on first use. A shared `.duckdb` file can hold views that read your
files or fetch a fixed URL when you query them.

## Secrets

### Where passwords come from

Each connection takes its password from one source:

| Source | Where it is kept |
| --- | --- |
| System keychain | macOS Keychain, Windows Credential Manager, or the Secret Service on Linux, under the service `DGopher` |
| Environment variable | Not stored. Read at each connect from a variable whose name starts with `DGOPHER_`. |
| Command | Not stored. A command such as `op read "op://Vault/DB/password"` prints it at each connect. |
| Ask every time | Not stored. Kept in memory until you disconnect. |
| Cloud identity | Not stored. A short-lived token from `aws`, `gcloud` or `az` (PostgreSQL and MySQL). |

- Before you save, the connection form says exactly where the password
  will be kept; for the keychain, the service and the account name.
- If no keychain is available, the app asks for passwords at each connect
  instead of storing them.
- Project files (`dgopher.json`) never hold passwords.
- Environment variables must start with `DGOPHER_`, so a cloned repository
  cannot make the app send, say, `GITHUB_TOKEN` to a host it chooses.
- The SSH password or key passphrase can come from the keychain or a
  command too. A proxy's password is kept in the keychain.

### Password commands

- A command runs directly, never through a shell, with a 30-second limit.
- Its output is never logged, saved or audited.
- When it fails, the app shows what it printed to stderr. The audit log and
  query history record only that it failed and its exit status.
- A command from a project file is shown and must be approved before it
  first runs.

### Connections from a project file

A connection you create or edit in the app is trusted when you save it. A
connection that arrived in `dgopher.json` from someone else asks before it
first connects. The prompt lists every field that decides where the
password goes and how safely:

- the server, TLS and whether it is verified, the CA file, and a client
  certificate;
- a proxy, SSH and jump hosts;
- where the password comes from: a variable, a command's exact text, or a
  cloud identity's command;
- clear-text passwords;
- the environment, read-only and the commit mode.

It asks again when any of these changes, such as after a `git pull`, and
marks what changed. It also asks when the environment, read-only or commit
mode would protect less (production to staging or development, read-only
off, manual commit to auto). A change that protects more is accepted
without asking.

Trust is kept in your own config, not the project, so a repository cannot
approve its own servers ([Where things are kept](storage.md)).

### A password goes only where it was typed for

A password in the keychain is tied to its destination: the engine, host,
port, user and database, the SSH tunnel and the Redis topology, and, when
set, the client certificate, jump hosts, proxy, cloud identity and
clear-text mode. A proxy's password is tied to the proxy too.

If any of these changes, for example when a pulled `dgopher.json` points
the connection at another host, the app finds no password for the new
destination and sends none. Enter the password again in the connection's
form. A password stored before this rule existed asks to be typed once,
and can then be kept again.

When a connection's ID changes, its password and trust move to the new ID.
The old entries are removed only after the new ones and `dgopher.json` are
written. Teammates who pull the new ID are asked again whether to trust
it.

### Rules for each protocol

- **SSH jump hosts** log in with a key file or the SSH agent. The SSH
  password goes only to the final SSH host.
- **PostgreSQL** never reads `~/.pgpass`, `~/.postgresql`'s certificates or
  your `PG*` environment variables. A `PGSERVICE` that names no service
  makes the connection fail rather than go elsewhere.
- **PostgreSQL clear-text passwords** are never sent under `prefer` or
  `require`. Neither mode checks the server's certificate, so the server
  asking could be anyone in the path. Such a server fails to connect; use
  verify-full.
- **Cloud identity tokens and MySQL clear-text passwords** are sent only
  over verified TLS.
- **MySQL `caching_sha2_password`** under `disable` and `prefer` fetches
  the server's public key without checking it, so an attacker in the path
  can learn the password. Where the network is not yours, use require, or
  better verify-full.
- **Backup tools** run with the app's settings only. `pg_dump` and
  `pg_restore` read none of the PostgreSQL files or variables above and
  keep the clear-text rule (from libpq 16). `mysqldump` reads no option
  file but the app's, and no `MYSQL_*` variables.

### Files on disk

Files in the app's directories are written atomically with mode 0600, in
directories with mode 0700.
