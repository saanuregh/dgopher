# Safety model

Every statement goes through one policy ([`internal/safety`](../internal/safety))
before it reaches the server:

| | Development | Staging | Production |
| --- | --- | --- | --- |
| Reads | run | run | run |
| Writes (INSERT, UPDATE, …) | run | run | confirm |
| Schema changes (CREATE, ALTER, …) | run | confirm | confirm |
| DROP, TRUNCATE, replacing a table (`CREATE OR REPLACE TABLE`), UPDATE/DELETE without WHERE or with one always true (`WHERE 1=1`, `WHERE TRUE`, `id = id`, `1=1 AND 2=2`, MySQL's `&&` too), also inside a `WITH`, ClickHouse ALTERs that lose rows (`CLEAR COLUMN`, `MODIFY TTL`, `REPLACE`/`MOVE`/`DETACH PARTITION`), `ALTER … TRUNCATE PARTITION`, Redis FLUSHALL/KEYS/… | confirm | confirm | confirm, by typing the connection's name |
| Default commit mode | auto | auto | manual (ClickHouse and Redis: always auto) |

Redis commands that write follow the Writes row. Commands that need a
connection of their own (`SELECT`, `AUTH`, `MULTI`, `SUBSCRIBE`, `MONITOR`
and similar) are refused everywhere, because the console and key browser
share a pool of connections.

**Read-only connections** refuse writes in two places. The app also
refuses statements that would switch read-only off: any that contains
`READ WRITE`, `set_config`, `read_only`, `readonly` or `query_only`, even in
a string or a quoted name:
- **In the app:** a statement the app cannot classify counts as a write.
- **On the server:** PostgreSQL `default_transaction_read_only`, MySQL
  `transaction_read_only`, ClickHouse `readonly=2`, SQLite `mode=ro` with `query_only`, and
  DuckDB `access_mode=READ_ONLY`.

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
- `EXPLAIN ANALYZE`, `RETURNING` and a `WITH` that changes rows, whose
  rows are read, not counted.

The count is the server's: an upsert's or a MERGE's includes the rows it
inserts, and MySQL counts a row that ON DUPLICATE KEY UPDATE changes
twice.

**On production, a script asks before each write** — Run, Skip, Run All
or Cancel; a destructive statement asks on its own even after Run All.

**Transactions:**
- Closing a tab, disconnecting, removing a project or quitting with an
  open transaction asks: Commit, Roll Back or Cancel.
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

**How statements are read:**
- The app splits a script into statements with the lexing rules of each
  server: its comments, quotes, escapes and dollar-quoted strings. Tests run
  these rules against the real servers.
- Text that still holds more than one statement counts as a destructive
  write, confirmed everywhere. On PostgreSQL the server also refuses such
  text, because statements run through the extended protocol.
- Creating a procedure, function or trigger whose body holds a destructive
  statement (DROP, TRUNCATE, UPDATE or DELETE without WHERE) is confirmed as
  that statement would be.
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
  without the statement's secrets wherever an error quotes them.
- Statements that change the server rather than the session count as
  writes: MySQL `RESET`, `SET GLOBAL`, `SET PERSIST`, `SET PASSWORD`,
  `SET DEFAULT ROLE` and `START REPLICA`, and PostgreSQL `COMMIT PREPARED`
  and `ROLLBACK PREPARED`.
- Read-only connections decode PostgreSQL `U&"…"`, `U&'…'` and `E'…'`
  escapes and ClickHouse backslash escapes before looking for the read-only
  settings, and refuse any `UESCAPE`.

For a guarantee that nothing can bypass, such as a `SELECT` calling a
function that writes and is not on the list, connect as a database role
that only has read privileges.

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
  second limit. Its output is never logged, saved or audited, and an error
  shows only what it printed to stderr. The SSH password or key passphrase
  can come from a command too.
- Files in the app's directories are written atomically with mode 0600, in
  0700 directories.
