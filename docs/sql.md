# How SQL is read

Everything DGopher does with SQL text starts from one lexer in
[`internal/sqltext`](../internal/sqltext): highlighting, finding the
statement at the caret, splitting a script, the
[safety policy](safety.md), parameters, completion and formatting. The
lexer never fails: half-typed SQL in the editor still produces tokens that
cover the whole text.

A wrong rule here can do more than color a word wrongly. If the app ends a
statement where the server does not, a `DROP` could reach the server inside
a statement the safety policy read as a `SELECT`. So the rules below follow
each server exactly, and a test checks them against the real servers.

- [Dialects](#dialects)
- [Lexing rules](#lexing-rules)
- [Splitting a script](#splitting-a-script)
- [Classification](#classification)
- [Names and values the app writes](#names-and-values-the-app-writes)
- [Parameters](#parameters)
- [Formatting](#formatting)
- [Tests](#tests)

## Dialects

| Engine | Lexer dialect |
| --- | --- |
| PostgreSQL | `Postgres` |
| DuckDB | `Postgres` (its parser derives from PostgreSQL's) |
| MySQL | `MySQL` |
| ClickHouse | `ClickHouse` |
| SQLite | `SQLite` |
| SQL Server | `TSQL` (Transact-SQL) |

Each engine's dialect is its rules' `LexDialect()`
([`internal/db/engine.go`](../internal/db/engine.go)); the engines built on
PostgreSQL and MySQL use their dialects.

## Lexing rules

These rules decide where strings, names and comments end, and so where
statements end.

| Rule | PostgreSQL, DuckDB | MySQL | ClickHouse | SQLite | T-SQL |
| --- | --- | --- | --- | --- | --- |
| `--` comment | always; ends at `\n` or `\r` | only before whitespace or a control character (`1--1` is 2) | always; ends at `\n` | always; ends at `\n` | always; ends at `\n` or `\r` |
| `#` comment | — | always | only before whitespace or `!` (`#x` is an error) | — | — (`#t` is a temporary table) |
| `/* */` nests | yes | no | yes | no | yes |
| `"…"` | name | string, `\` escapes | name, `\` escapes | name | name |
| `` `…` `` | — | name | name, `\` escapes | name | — |
| `[…]` | — | — | — | name | name; `]]` is one `]` |
| `'…'` escapes | doubled quote; `E'…'` takes `\` | `\` and doubled quote | `\` and doubled quote | doubled quote | doubled quote; `N'…'` the same |
| `$$…$$`, `$tag$…$tag$` | string | — | string | — | — |
| `$` inside a name | yes | yes | yes | yes | yes, and `@` and `#` |

In every dialect:

- A character outside ASCII belongs to a name, but for the spaces below.
- ASCII spaces, tabs, line breaks, form feeds and vertical tabs separate
  words (CockroachDB, YugabyteDB and DuckDB refuse a vertical tab, and
  SQLite one that follows no other space). PostgreSQL, MySQL and the
  engines built on them read no other character as a space. SQL Server
  also reads every other control
  character below U+0020, and U+0085, U+00A0, U+1680, U+2000 to U+200B,
  U+2028, U+2029, U+202F, U+205F and U+3000; ClickHouse U+0085, U+00A0,
  U+180E, U+2000 to U+200D, U+2028, U+2029, U+202F, U+205F, U+2060,
  U+3000 and U+FEFF; SQLite U+FEFF where a token starts. The lexer reads
  them so.
- DuckDB reads U+00A0, U+2000 to U+200B, U+202F, U+205F, U+2060, U+3000
  and U+FEFF as spaces, and PostgreSQL as part of a name, with one lexer
  for both: a statement holding a Unicode space outside strings, quoted
  names and comments counts as a write. So does a T-SQL batch holding a
  control character or U+200B there: SQL Server runs a batch without an
  ASCII space, as `SELECT<U+0001>1`, as a call of the procedure the whole
  text names.
- A multi-character operator never swallows the start of a comment, so
  `//* c */` keeps its comment.

In T-SQL:

- `@name` and `@@name` are the server's variables and functions, not
  parameters. `#t` and `##t` are temporary tables.
- `$12.50` is a money value, `0x1F` (and `0x` alone) a binary one, and
  `$action` or `$PARTITION` a name. `x'…'` is a name followed by a string.
- A `--` comment also ends at a lone `\r`. This reads more code than the
  server may run, never less.

[`internal/db/lexing_conformance_test.go`](../internal/db/lexing_conformance_test.go)
runs a query per rule on each server and checks that the lexer agrees.

### Settings that change the rules

A session, role, database or server setting can change where strings end:

- PostgreSQL's `standard_conforming_strings = off` makes `\` an escape.
- MySQL's `NO_BACKSLASH_ESCAPES` makes `\` a plain character.
- MySQL's `ANSI_QUOTES` makes `"…"` a name.
- SQL Server's `SET QUOTED_IDENTIFIER OFF` makes `"…"` a string. It ends
  where the name would, so nothing splits differently, and the policy reads
  a `"…"` given to `EXECUTE` as code either way.

For the safety policy, a statement that holds a `\` (or, on MySQL, a `"`)
is read under every rule its server can switch to; DuckDB counts as
PostgreSQL here. It counts as its most dangerous reading.

## Splitting a script

A statement ends at `;`, or at a line that holds only whitespace. A
setting (Settings → Editor) can make `;` the only end. A blank line inside
parentheses or a `CASE … END` does not end a statement.

- **Routine bodies.** In `CREATE PROCEDURE`, `FUNCTION`, `TRIGGER` or
  `EVENT`, a `BEGIN … END` body (and PostgreSQL's `BEGIN ATOMIC`) keeps its
  `;`s. If the body never closes, the text splits at every `;` instead, so
  an unfinished body cannot hide the statements after it. `BEGIN` and
  `END` used as names do not open or close a body.
- **MySQL `DELIMITER`.** A line `DELIMITER //` sets the delimiter to its
  first word, as the `mysql` client does. The line itself is not sent.
  `DELIMITER ;` restores the default.
- **`BEGIN`** at the start of a statement begins a transaction (in T-SQL,
  only `BEGIN TRAN[SACTION]`).

### T-SQL batches

The editor runs T-SQL a batch at a time, as SSMS does: `;` ends no
statement, so a variable declared in a batch is in scope through all of
it.

- **`GO`.** A line holding only `GO`, in any case, ends a batch, as
  `sqlcmd` and SSMS read it. Whitespace and comments that end on the line
  may follow it. The line itself is not sent. `GO` inside a string, comment
  or `[name]` is text, and so is `GO;` or a `GO` with code on its line.
- **`GO n`.** A `GO` with a count other than 1 stays a statement of its own.
  DGopher runs each batch once, so running it asks first, and the server
  rejects it.
- **Blank lines** split a batch as in other dialects, when the setting
  says so, except where the rules below keep it together.
- **Blocks.** Every `BEGIN … END`, `BEGIN TRY … END TRY` and
  `BEGIN CATCH … END CATCH` keeps its blank lines. `BEGIN TRAN`,
  `BEGIN DISTRIBUTED TRANSACTION` and `BEGIN DIALOG` are statements, and
  `END CONVERSATION` closes nothing.
- **Routines.** `CREATE [OR ALTER]` or `ALTER` of a `PROCEDURE`, `FUNCTION`
  or `TRIGGER` runs to the end of its batch, as its body does on the
  server. A view splits as other statements do.
- **Control flow.** An `IF`, `WHILE` or `ELSE` keeps the statement it runs
  across a blank line, and a statement keeps an `ELSE` after it.

## Classification

Each statement gets a class (read, write, schema change, transaction or
session), its verb, and whether it is destructive. The safety policy
decides from these. The rules are in
[`classify.go`](../internal/sqltext/classify.go).

### General rules

- **Unknown verbs** count as writes.
- **More than one statement** in a text the policy receives counts as a
  destructive write. On PostgreSQL the server refuses such text too,
  because statements without parameters run through the extended
  protocol. A T-SQL batch is one statement here; only text holding
  several batches, split by `GO`, counts so.
- **Always-true conditions** are read through casts: `TRUE::bool`,
  `1::int = 1::int` and `CAST(1 AS bool)` are always true, so an `UPDATE`
  or `DELETE` with such a `WHERE` is destructive.

### Code the server runs

Code keeps the danger of what it holds. The app reads the statements
inside it and asks as it would for the most destructive one (`DROP`,
`TRUNCATE`, or `UPDATE` or `DELETE` without a real `WHERE`). This covers:

- a procedure, function or trigger body, whether written `BEGIN … END`,
  `BEGIN ATOMIC`, or quoted (`$$…$$`, `$tag$…$tag$`, `'…'`, `E'…'`);
- a PostgreSQL `DO` block;
- a string after `EXECUTE` inside such code, including strings joined with
  `||`;
- `PREPARE … AS`, and MySQL's `PREPARE … FROM '…'`;
- a `CREATE RULE`'s `DO [ALSO | INSTEAD]` action.

Limits:

- Only bodies in SQL or PL/pgSQL are read, not other languages.
- Code quoted more than 8 levels deep counts as destructive. So does more
  quoted code than 16 times the statement's length plus 1 KB.
- A string after `EXECUTE` that uses `UESCAPE` counts as destructive.
- A `MERGE`'s `THEN DELETE` and an upsert's `DO UPDATE` inside a body are
  not statements of their own.
- SQL built at run time, such as `EXECUTE format(…)`, cannot be known.

### Writes hidden in reads

These count as writes:

- `INTO` a table or file after `SELECT`, `TABLE` or `VALUES`.
- `EXPLAIN ANALYZE` and `DESC ANALYZE` of a write, and MariaDB's `ANALYZE`
  of a write, such as `ANALYZE UPDATE …`, which runs it. EXPLAIN's options
  are read as names, quoted or not, so `EXPLAIN ("analyze") DELETE …`
  counts too; a `U&` name or a `UESCAPE` among them counts as every
  option.
- CockroachDB's `EXPLAIN … (DEBUG)`, which keeps a statement bundle on the
  server.
- What a CockroachDB `[ <statement> ]` data source runs, such as
  `SELECT * FROM [DELETE FROM t RETURNING id]`. Array subscripts
  (`a[1]`, `ARRAY[…]`) and DuckDB lists (`[start, stop]`) are not
  statements.
- A `WITH` (CTE) that changes data.
- A call to a function with side effects. Examples:
  `pg_terminate_backend`, `pg_cancel_backend`, `setval`, `nextval`,
  `set_config`, `pg_notify`, `dblink_exec`, `lo_import`, `lo_export`, the
  advisory locks, replication slots and origins, `pg_stat_reset`; MySQL
  `GET_LOCK`, `RELEASE_LOCK`, `SLEEP` and `BENCHMARK`; MariaDB's sequence
  functions `NEXTVAL`, `SETVAL` and `LASTVAL`, and `NEXT VALUE FOR`; SQLite
  `load_extension`. The full list per engine is in `classify.go`. Quoted
  and escaped function names are decoded first.
- Any call of a CockroachDB `crdb_internal` function but the known reads
  (`crdbInternalReads` in `classify.go`), such as `node_id`, `cluster_id`
  and the `show_create_all_*` functions.
- Any call of a function named `yb_`, YugabyteDB's, but the known reads
  (`ybReads` in `classify.go`), such as `yb_servers`,
  `yb_table_properties` and `yb_lock_status`. A PostgreSQL function of
  your own whose name starts with `yb_` counts too.
- A call of TimescaleDB's functions that change hypertables, chunks,
  jobs, policies, data nodes or the restore state (`timescaleWrites` in
  `classify.go`), such as `create_hypertable`, `drop_chunks`,
  `compress_chunk`, `add_retention_policy`, `alter_job`,
  `set_adaptive_chunking` and `timescaledb_pre_restore`, and any function
  of its internal schemas, whose names start with `_timescaledb_`. A
  PostgreSQL function of your own of one of those names counts too.
- `SELECT … FOR UPDATE` or `FOR SHARE` (MySQL: `LOCK IN SHARE MODE`), in a
  subquery or a `WITH` too. It locks its rows for as long as its result,
  or its transaction, is open.
- ClickHouse's `MOVE` (of users and roles between storages). PostgreSQL's
  `MOVE` only moves a cursor.
- On MySQL, MariaDB and TiDB, what an executable comment (`/*! … */`,
  `/*!80000 … */`, MariaDB's `/*M! … */`, TiDB's `/*T! … */` and
  `/*T![feature] … */`) holds, and what it hides. A server older than the
  comment's version skips it, TiDB skips a `/*T![feature] … */` naming a
  feature it lacks, and each server skips the others' kinds, so the
  statement is read with each comment run and skipped, each feature list
  in every combination, and the strictest reading counts. A statement
  whose comments name more versions or feature lists than are read
  (`maxExecutableVersions` and `maxExecutableFeatureLists` in
  `executable.go`) also asks first.

### Statements that change the server

These change the server rather than the session, so they count as writes:

- MySQL `RESET`, `SET GLOBAL`, `SET PERSIST`, `SET PASSWORD`,
  `SET DEFAULT ROLE` and `START REPLICA`.
- TiDB `SET CONFIG`, which configures a cluster component, and
  `SET BINDING`, which switches a global plan binding.
- PostgreSQL `COMMIT PREPARED` and `ROLLBACK PREPARED`.
- CockroachDB `SET CLUSTER SETTING` and `RESET CLUSTER SETTING`.
- A `SET search_path` or `SET schema` that names `crdb_internal` or a
  schema starting with `_timescaledb_`, whose functions it then lets be
  called by their names alone, and a `SET` or `RESET` of
  `allow_unsafe_internals`, which opens `crdb_internal` and `system` to
  the session.
- A YugabyteDB `SET` or `RESET` of `yb_disable_transactional_writes`, which
  writes outside transactions, or `yb_enable_upsert_mode`, which makes
  `INSERT` overwrite rows, skipping unique and foreign key checks.
- A TimescaleDB `SET` or `RESET` of `timescaledb.restoring`, which turns
  off what makes hypertables work: a row inserted then lands in no chunk.
- DuckDB `SET`s of a setting that names a file it writes, such as
  `log_query_path`, `profiling_output`, `temp_directory`,
  `extension_directory`, `secret_directory` and `home_directory`.

### T-SQL

A batch is read as every statement it holds, with or without `;`
between them, and counts as the strictest of them:
`SELECT 1 DELETE FROM t` is a `DELETE` without `WHERE`. A statement word
that starts none, as the `DELETE` of `GRANT DELETE` or
`ON DELETE CASCADE`, still counts toward the class. The rules are in
[`tsql.go`](../internal/sqltext/tsql.go).

- **Reads:** `SELECT`, `DECLARE`, `PRINT`, `SET @variable`, control flow
  (`IF`, `WHILE`, `BEGIN … END`, `TRY`/`CATCH`, `RETURN`, `WAITFOR DELAY`),
  `THROW`, `RAISERROR` without `WITH LOG`, cursor `OPEN`, `FETCH`, `CLOSE`
  and `DEALLOCATE`, and `KILL … WITH STATUSONLY`. Also the system
  procedures known to read (`tsqlProcedureReads`), such as `sp_help`,
  `sp_helptext`, `sp_columns`, `sp_who2` and `xp_fixeddrives`, called by
  their name alone or in the `sys` schema; `sp_configure` and
  `sp_spaceused` only with at most their first argument. And the `DBCC`
  checks and reports (`dbccReads`), such as `CHECKDB` and `SQLPERF`,
  without a `REPAIR_` option or `CLEAR`, and `CHECKIDENT … NORESEED`.
- **Session:** `SET` options, `USE`, `EXECUTE AS`, `REVERT`, `SETUSER`, and
  opening or closing a symmetric or master key.
- **Transactions:** `BEGIN TRAN[SACTION]`, `BEGIN DISTRIBUTED TRAN`,
  `COMMIT`, `ROLLBACK` and `SAVE TRAN`.
- **Writes:** `EXECUTE` of any other procedure, and a procedure named
  first in a batch without `EXECUTE`, such as `sp_configure 'x', 1`;
  `SELECT … INTO`, a `#temp` table too; a table hint that holds its locks
  (`UPDLOCK`, `XLOCK`, `HOLDLOCK`, `TABLOCKX`, `SERIALIZABLE`,
  `REPEATABLEREAD`); `OPENROWSET`, `OPENQUERY` and `OPENDATASOURCE`;
  `NEXT VALUE FOR`; `BULK INSERT`, `BACKUP`, `RESTORE`, other `DBCC`
  commands, `KILL`, `RECONFIGURE`, `CHECKPOINT`, `UPDATE STATISTICS`,
  `WRITETEXT`, `UPDATETEXT`, `RAISERROR … WITH LOG`, and Service Broker's
  `RECEIVE`, `SEND`, `BEGIN DIALOG`, `END CONVERSATION`, `MOVE` and
  `GET CONVERSATION`. A column or table named `receive` counts too.
- **Schema changes:** `CREATE`, `ALTER`, `DROP`, `TRUNCATE`, `GRANT`,
  `DENY`, `REVOKE`, and `ENABLE` or `DISABLE TRIGGER`.
- **Destructive**, besides the general rules: `ALTER TABLE … SWITCH`,
  `RESTORE DATABASE` and `RESTORE LOG`, `SHUTDOWN`, `xp_cmdshell`,
  `DBCC … REPAIR_ALLOW_DATA_LOSS`, `DBCC WRITEPAGE`, and `GO n`.
- **Code:** the strings given to `EXECUTE ( … )`, joined with `+`, and to
  `sp_executesql`, `sp_MSforeachtable` and `sp_MSforeachdb` are read as
  T-SQL and keep their danger. A procedure body keeps the danger of its
  statements. SQL held in a variable cannot be known.

### Reads

Reads include `SHOW`, `DESCRIBE`, `EXPLAIN`, `FETCH`, cursor declarations
over a query, DuckDB `SUMMARIZE`, `PIVOT` and `FROM`-first queries,
`CHECK TABLE`, and the SQLite and DuckDB `PRAGMA`s that only report
(`table_info`, `index_list` and similar). Any `PRAGMA` with `=` is a
write.

### Secrets

History and the audit log redact secrets from statements, and from errors
that quote them. Secrets are found with the PostgreSQL, MySQL and
ClickHouse lexers together, so a backslash-escaped quote cannot cut one
short. The redactor ([`internal/redact`](../internal/redact)) hides:

- strings after `PASSWORD`, `IDENTIFIED … BY`, `SECRET`, `TOKEN`,
  `SESSION_TOKEN`, `CONNECTION_STRING` and `EXTRA_HTTP_HEADERS`;
- the arguments of functions and table engines that take credentials, such
  as `dblink`, `mysql()`, `s3()`, `MaterializedPostgreSQL` and `S3Queue`;
- any string that holds `password=`, `pwd=`, `passwd=`, `AccountKey=`,
  `motherduck_token=` or `Bearer `;
- the password in any `scheme://user:password@` URL;
- Redis `AUTH`, `CONFIG SET` passwords, `ACL SETUSER` rules and `SENTINEL`
  passwords.

## Names and values the app writes

The SQL the app generates (filters, generated statements, Go to Referenced
Row, SQL exports) quotes names and values so that no name or value can end
them early:

| | PostgreSQL, DuckDB | MySQL | ClickHouse | SQLite |
| --- | --- | --- | --- | --- |
| Name | `"…"`, `"` doubled | `` `…` ``, `` ` `` doubled; `\` is itself | `` `…` ``, `` ` `` doubled and `\` escaped | `"…"`, `"` doubled |
| String | `'…'`, `'` doubled; with a `\`, `E'…'` with `\\`; a NUL joined in as `('…' \|\| chr(0) \|\| '…')` | `\\`, `''`, `\0`, `\n`, `\r`, `\Z` | `\\`, `\'` and `\0` | `'…'`, `'` doubled; a NUL joined in as `char(0)` |
| Bytes | PostgreSQL `E'\\x…'::bytea`, DuckDB `'\x…'::BLOB` | `X'…'` | `unhex('…')` | `X'…'` |

Why these choices:

- A PostgreSQL string with a backslash is written `E'…'`, so it reads the
  same whether `standard_conforming_strings` is on or off.
- PostgreSQL text cannot hold a NUL, so such a value fails to load there.
- Binary data is always written as bytes, even when it would read as text.
  The app tells binary from text by the column's type (on SQLite, which
  keeps each value's own type, by the value's). Written as text,
  PostgreSQL would load different bytes and SQLite would store text.
- MySQL strings double their quotes, so a dump also loads under
  `NO_BACKSLASH_ESCAPES`.

## Parameters

| Syntax | Engines | Sent as |
| --- | --- | --- |
| `:name` | all | a placeholder with its value |
| `@name`, `$name` | SQLite | a placeholder with its value |
| `{name:Type}` | ClickHouse | a server-side query parameter; the text is unchanged |
| `${var}` | all | written into the text, which is then checked again |

- In T-SQL, `@name` is the server's variable, not a parameter, and a `:`
  right after a name, as a label's, starts none.
- A statement that mixes `:name` with `$1` or `?` placeholders is refused.
- You type values in the editor's Parameters bar. A run asks for any value
  left empty there, unless its kind is NULL. The kind you choose is kept.
- Values are kept per connection in the project's own state
  (`.dgopher/state.sqlite`), never in `dgopher.json`, and follow a change
  of the connection's ID. Dashboards keep their values apart, per
  dashboard.

The code is [`internal/sqltext/params.go`](../internal/sqltext/params.go)
and [`internal/params`](../internal/params/params.go).

## Formatting

- The formatter changes only whitespace and the case of keywords. Every
  other token keeps its text and order.
- ClickHouse and MySQL keep names in the case they were written, so there
  only clause, join and operator words are upper-cased.
- On T-SQL only reserved words are upper-cased, as a case-sensitive
  collation tells other words apart by case. `[names]` are kept as written.
- It never adds a space that would create a comment, such as between
  MySQL's `-` `-`.

## Tests

| Command | Checks |
| --- | --- |
| `go test ./internal/sqltext` | the lexer, splitter, classifier, parameters, completion and formatter |
| `go test ./internal/sqltext -run '^$' -fuzz '^FuzzTokenize$' -fuzztime 60s` | that tokens cover the text and formatting keeps every token |
| `DGOPHER_IT=1 go test ./internal/db -p 1 -run LexingConformance` | the lexing rules against the real servers |
| `go test ./internal/sqltext -run '^$' -bench Tokenize -benchmem` | the lexer's speed |
