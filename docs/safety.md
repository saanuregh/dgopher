# Safety model

Every statement goes through one policy ([`internal/safety`](../internal/safety))
before it reaches the server:

| | Development | Staging | Production |
| --- | --- | --- | --- |
| Reads | run | run | run |
| Writes (INSERT, UPDATE, …) | run | run | confirm |
| Schema changes (CREATE, ALTER, …) | run | confirm | confirm |
| DROP, TRUNCATE, replacing a table (`CREATE OR REPLACE TABLE`), UPDATE/DELETE without WHERE or with one always true (`WHERE 1=1`, `WHERE TRUE`, `id = id`, `1=1 AND 2=2`, MySQL's `&&` too, casts as `TRUE::bool` or `CAST(1 AS bool)` too), also inside a `WITH`, ClickHouse ALTERs that lose rows (`CLEAR COLUMN`, `MODIFY TTL`, `MATERIALIZE TTL`, `REPLACE PARTITION`, `DETACH PARTITION`, `MOVE PARTITION … TO TABLE`), `ALTER … TRUNCATE PARTITION`, Redis FLUSHALL/KEYS/… | confirm | confirm | confirm, by typing the connection's name |
| Default commit mode | auto | auto | manual (ClickHouse and Redis: always auto) |

Redis commands that write follow the Writes row. Commands that need a
connection of their own (`SELECT`, `AUTH`, `MULTI`, `SUBSCRIBE`, `MONITOR`
and similar) are refused everywhere, because the console and key browser
share a pool of connections.

![On a production connection, a DELETE without WHERE asks for the
connection's name to be typed before it runs](images/production-confirm.png)

**Read-only connections** refuse writes in two places. The app also
refuses statements that would switch read-only off: any that contains
`READ WRITE`, `set_config`, `read_only`, `readonly`, `query_only`,
`access_mode` or `read_write`, even in a string or a quoted name:
- **In the app:** a statement the app cannot classify counts as a write.
- **On the server:** PostgreSQL `default_transaction_read_only`, MySQL
  `transaction_read_only`, ClickHouse `readonly=2`, SQLite `mode=ro` with `query_only`, and
  DuckDB `access_mode=READ_ONLY`. A PostgreSQL session can turn its setting
  off with `SET`, and DuckDB's does not cover a database attached
  `READ_WRITE`: the app refuses both statements, so for these the app's check
  is the one that holds.

**A large change asks before it commits.** On staging and production in
auto-commit, an UPDATE, DELETE, MERGE, REPLACE or upsert (`ON CONFLICT
DO UPDATE`, `ON DUPLICATE KEY UPDATE`, `INSERT OR REPLACE`) runs in a
transaction of its own; if it changed more rows than the limit
(Settings → Large changes, 1,000 by default, 0 for none), the app asks
whether to commit, and rolls it back unless you agree; a stopped run
rolls back, the script stops either way, and closing the editor while
asked says what it rolls back. Manual
commit leaves the transaction open anyway, its count shown. Not held:
- DuckDB and in-memory SQLite, whose sessions share one connection and
  one transaction, which the change would take from the other editors;
- a MySQL table that keeps no transactions, as MyISAM (a rollback that
  could not undo such a table's changes says so); MySQL counts the rows
  matched, changed or not;
- a MySQL session with autocommit off, whose change waits for your COMMIT
  anyway;
- `EXPLAIN ANALYZE`, `RETURNING` and a `WITH` that changes rows, whose
  rows are read, not counted;
- Run SQL File, imports, copies and generated rows, which ask once for all
  they will run (below).

The count is the server's: an upsert's or a MERGE's includes the rows it
inserts, and MySQL counts a row that ON DUPLICATE KEY UPDATE changes
twice.

**On production, an editor's script asks before each write** — Run, Skip,
Run All or Cancel; a destructive statement, or one that commits the open
transaction, asks on its own even after Run All.

**Run SQL File** reads the whole file first and asks once for all of it;
on production, a file that writes asks for the connection's name. It names
a statement that commits a transaction the file began itself (MySQL's
DDL), as what ran before it stays if the file fails later. It runs
in one transaction, all or nothing, where the engine can hold one and the
file begins and ends none of its own. Each statement is checked against
what was agreed as it runs. A file that stops with a transaction it began
still open (on MySQL, as the server reports it, so a file that turns
autocommit off and commits ends well) is rolled back, the rollback audited,
and the run fails naming the line that began it.

**Transactions:**
- The transaction shown is the one the server or its driver reports
  (MySQL's status flags, SQLite's autocommit, DuckDB's transaction id,
  PostgreSQL's status), however the statement was written: a comment before
  `BEGIN`, `ROLLBACK TO SAVEPOINT`, `COMMIT AND CHAIN` and `XA START`
  leave it open, and a deadlock or an implicit commit ends it. MySQL's
  `SET autocommit = 0` opens none: the next statement that reads or writes
  a table does. Until then the editor's bar says autocommit is off, and
  closing or quitting asks nothing. Its writes wait for COMMIT as manual
  commit's do: a statement in the run that would commit them implicitly
  asks first, and grid edits stay in the transaction they open.
- In manual commit, every write runs in a transaction: a run begins one
  before its first statement when none is open, and again after a
  statement that committed it implicitly, saying so.
- Closing a tab, disconnecting, removing a project or quitting with an
  open transaction asks: Commit, Roll Back or Cancel.
- Disconnecting, removing a project, deleting a connection or quitting
  while work runs on it (Run SQL File, an import, a copy, generated rows, a
  row comparison's apply, a backup or restore, an export, a schema change
  being applied, Redis bulk work) lists that work and what stopping it
  loses, and asks; agreed, the app stops it and waits up to 10 seconds for
  it to roll back what it left open.
- Closing a tab closes its connection: the next tab inherits no schema,
  `search_path`, role, variables, locks, temporary tables or transaction.
- Cancelling a MySQL statement (Esc, Stop, the statement timeout) sends
  `KILL QUERY` from a connection of its own, outside the pool, which stops
  the statement and keeps the session: its transaction, variables, `USE`
  and temporary tables. When the KILL cannot be sent, or the statement has
  not ended 5 seconds after it, the connection is dropped instead. The KILL
  names the session's connection as MySQL numbers it, which a proxy that
  shares server connections (ProxySQL, RDS Proxy) may number otherwise.
- After a lost connection (a network drop, `KILL`, `pg_terminate_backend`,
  a MySQL cancel that dropped the connection) the next statement is refused once: the
  connection was made again, and its schema, `search_path`, role, variables
  and temporary tables were reset. Running it again runs it on the new
  connection; the editor shows the schema it is in now. A script, even one
  set to go on past errors, and Run SQL File stop there, so the rest never
  runs without what the lost connection had set; a grid apply that loses
  its connection leaves the refusal for your next statement.
- DuckDB and in-memory SQLite share one connection, so one transaction: it
  belongs to the tab that began it. Other tabs say they run inside it, and
  their Commit and Roll Back refuse and name that tab, as do their typed
  `COMMIT`, `END`, `ROLLBACK` (not `ROLLBACK TO`), `ABORT`, `BEGIN` and
  `START TRANSACTION`, before a run and again as each such statement
  comes, should another tab have begun a transaction while the run went
  on; the status bar, the idle warning and the close and
  quit prompts name the owner only. Their work counts as the owner's use of
  the transaction, so it does not roll back as idle while they work. Every
  tab reads the shared transaction again each frame, so the owner sees
  another tab's statement end or fail it; a Commit or Roll Back sends
  nothing when the transaction open now is another tab's, or none is. A
  schema change that runs in a transaction of its own (the table designer,
  a model's migration) is refused while one is open, as its `BEGIN` would
  fail and abort it.
- On MySQL, a statement that commits an open transaction itself (DDL and
  the rest of [MySQL's list](https://dev.mysql.com/doc/refman/8.4/en/implicit-commit.html),
  temporary tables aside) asks first: Roll Back would not undo what ran
  before it. With none open yet, it asks when a write comes before it in
  the same run inside a transaction: the app's, in manual commit, or one
  the run itself begins with `BEGIN` or `START TRANSACTION`.
- Grid changes applied inside an open transaction go behind a savepoint: a
  failed apply rolls back to it and keeps the transaction. DuckDB has no
  savepoints, so there they are refused while a transaction is open: a
  failed change would abort all of it.
- A transaction left idle rolls back, after a warning you can cancel:
  after 10 minutes on production, 15 on staging and 30 on development, or
  as the connection says.
- The status bar lists every open transaction and how long it has been
  open.
- Deleting rows from the grid names the tables whose foreign keys point at
  them.
- On production, applying grid changes or a row comparison's changes that
  update or delete more rows already there than the large-change limit
  asks for the connection's name, as an UPDATE without WHERE does.
- Reading rows again while changes are not applied yet (refresh, sort,
  filter, fetching more, running the editor again) asks first: Apply,
  Discard or Cancel.
- A result whose statement does more than read, as one calling `nextval`
  or one `FOR UPDATE`, cannot be edited, and says why: reading it again
  would run it again.
- Server activity's Cancel query and End session (and Redis's `CLIENT
  KILL`) go through the policy as if the connection were not read-only, so a
  read-only monitoring connection can still stop work; the server's
  privileges decide. They always ask, and End session on production asks
  for the connection's name.

**How statements are read:**
- The app splits a script into statements with the lexing rules of each
  server: its comments, quotes, escapes and dollar-quoted strings. Tests run
  these rules against the real servers.
- Text that still holds more than one statement counts as a destructive
  write, confirmed everywhere. On PostgreSQL the server also refuses such
  text, because statements run through the extended protocol.
- Code the server runs is read as the statements it holds and confirmed as
  its most destructive one would be (DROP, TRUNCATE, UPDATE or DELETE
  without WHERE or with one always true): a procedure, function or trigger's
  body, in `BEGIN … END`, `BEGIN ATOMIC` or quoted (`$$…$$`, `$tag$…$tag$`,
  `'…'`, `E'…'`), a PostgreSQL `DO` block, a string after `EXECUTE` in such
  code (strings joined with `||` together), `PREPARE … AS` and MySQL's
  `PREPARE … FROM '…'`, and a rule's `DO [ALSO | INSTEAD]` action. Bodies in
  SQL or PL/pgSQL are read, not those in other languages; code quoted more
  than 8 levels deep, or more of it than 16 times the statement's length,
  counts as destructive. SQL built at run time, as
  `EXECUTE format(…)`, cannot be known.
- A `SELECT` that calls a function with side effects counts as a write, for
  example `pg_terminate_backend`, `setval`, `nextval`, `dblink_exec`,
  `lo_export`, the advisory locks, replication slots and origins,
  `pg_stat_reset`, MySQL `GET_LOCK` and `SLEEP`, and SQLite
  `load_extension`. Quoted and escaped function names are decoded first.
- A `SELECT … FOR UPDATE` or `FOR SHARE` (MySQL: `LOCK IN SHARE MODE`),
  in a subquery or a `WITH` too, counts as a write: it locks its rows for
  as long as its result is open, or its transaction is.
- ClickHouse's `MOVE` (of users and roles between storages) counts as a
  write; PostgreSQL's moves a cursor.
- History and the audit log keep errors redacted as statements are, and
  without the statement's secrets wherever an error quotes them. Secrets are
  found with the PostgreSQL, MySQL and ClickHouse lexers together, so a
  backslash-escaped quote cannot cut one short: strings after `PASSWORD`,
  `IDENTIFIED … BY`, `SECRET`, `TOKEN`, `CONNECTION_STRING` and
  `EXTRA_HTTP_HEADERS`, the arguments of functions and engines that take
  credentials (`dblink`, `mysql()`, `s3()`, `MaterializedPostgreSQL`,
  `S3Queue` and the like), any string holding `password=`, `pwd=`,
  `passwd=`, `AccountKey=`, `motherduck_token=` or `Bearer `, the password of
  any `scheme://user:password@` URL, and Redis's `AUTH`, `CONFIG SET`
  passwords, `ACL SETUSER` rules and `SENTINEL` passwords.
- Statements that change the server rather than the session count as
  writes: MySQL `RESET`, `SET GLOBAL`, `SET PERSIST`, `SET PASSWORD`,
  `SET DEFAULT ROLE` and `START REPLICA`, PostgreSQL `COMMIT PREPARED`
  and `ROLLBACK PREPARED`, and DuckDB settings that name a file it writes
  (`log_query_path`, `profiling_output`, `temp_directory`,
  `extension_directory`, `secret_directory`, `home_directory`, …).
- Read-only connections decode PostgreSQL `U&"…"`, `U&'…'` and `E'…'`
  escapes and ClickHouse backslash escapes before looking for the read-only
  settings, and refuse any `UESCAPE`.

For a guarantee that nothing can bypass, such as a `SELECT` calling a
function that writes and is not on the list, connect as a database role
that only has read privileges. DuckDB reads local files and, by default,
installs extensions from the network on first use: a shared `.duckdb` file
can hold views that read your files or fetch a fixed URL when queried.

## Secrets

Each connection takes its password from one place.

| Source | Where it is kept |
| --- | --- |
| System keychain | macOS Keychain, Windows Credential Manager, or the Secret Service on Linux, under the service `DGopher` |
| Environment variable | not stored; read from a `DGOPHER_*` variable at each connect |
| Command | not stored; a command such as `op read "op://Vault/DB/password"` prints it at each connect |
| Ask every time | not stored; kept in memory until you disconnect |

- Before you save, the connection form says exactly where the password
  will be kept: for the keychain, the service and the account name.
- If no keychain is available, passwords are asked on each connect instead
  of being stored.
- A password command runs directly, never through a shell, with a 30
  second limit. Its output is never logged, saved or audited. When it
  fails, what it printed to stderr is shown, but the audit log records only
  that it failed and its exit status, as does query history. The SSH
  password or key passphrase can
  come from a command too, and the same holds for a cloud identity's command.
- A connection from a project's file (`dgopher.json`) asks before it first
  connects, listing every field that decides where its password goes and
  how safely: the server, TLS (and whether it is verified), the CA file, a
  client certificate, a proxy, SSH and jump hosts, where the password comes
  from (with a command's text, or the cloud identity's command), clear-text
  passwords, the environment, read-only and the commit mode. It asks again
  when any of them changes, as in a pull, marking what changed, and when the
  environment, read-only or commit mode would protect less.
- A password kept in the keychain belongs to where it goes: the server,
  SSH, Redis topology, and when set the client certificate, jump hosts,
  proxy, cloud identity and clear-text mode. A change to any of them finds
  no password for the new destination, so none is sent there; enter it in
  the connection's form. A password kept before this rule asks to be typed
  once, and can be kept again.
- Jump hosts log in with the key file or the SSH agent; the SSH password goes
  only to the SSH host.
- A connection uses only the password its source gives: PostgreSQL
  connections never read `~/.pgpass`, `~/.postgresql`'s certificates or the
  `PG*` variables of your environment (a `PGSERVICE` naming no service
  makes them fail rather than go elsewhere), and the backup tools run with
  the app's settings only: pg_dump and pg_restore read none of these
  either, and mysqldump reads no option file but the app's, nor `MYSQL_*`
  variables.
- A cloud identity's token and a MySQL password sent as clear text go only
  over verified TLS. Under `disable` and `prefer`, MySQL's
  `caching_sha2_password` fetches the server's key without checking it, so
  an attacker in the path can learn the password: use require, better
  verify-full, where the network is not yours.
- Files in the app's directories are written atomically with mode 0600, in
  0700 directories.
