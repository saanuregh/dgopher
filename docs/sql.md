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

Each engine's dialect is set in the `lex` field of its entry in
[`internal/db/config.go`](../internal/db/config.go).

## Lexing rules

These rules decide where strings, names and comments end, and so where
statements end.

| Rule | PostgreSQL, DuckDB | MySQL | ClickHouse | SQLite |
| --- | --- | --- | --- | --- |
| `--` comment | always; ends at `\n` or `\r` | only before whitespace or a control character (`1--1` is 2) | always; ends at `\n` | always; ends at `\n` |
| `#` comment | — | always | only before whitespace or `!` (`#x` is an error) | — |
| `/* */` nests | yes | no | yes | no |
| `"…"` | name | string, `\` escapes | name, `\` escapes | name |
| `` `…` `` | — | name | name, `\` escapes | name |
| `[…]` | — | — | — | name |
| `'…'` escapes | doubled quote; `E'…'` takes `\` | `\` and doubled quote | `\` and doubled quote | doubled quote |
| `$$…$$`, `$tag$…$tag$` | string | — | string | — |
| `$` inside a name | yes | yes | yes | yes |

In every dialect:

- A character outside ASCII belongs to a name.
- Only ASCII spaces, tabs and line breaks separate words.
- A multi-character operator never swallows the start of a comment, so
  `//* c */` keeps its comment.

[`internal/db/lexing_conformance_test.go`](../internal/db/lexing_conformance_test.go)
runs a query per rule on each server and checks that the lexer agrees.

### Settings that change the rules

A session, role, database or server setting can change where strings end:

- PostgreSQL's `standard_conforming_strings = off` makes `\` an escape.
- MySQL's `NO_BACKSLASH_ESCAPES` makes `\` a plain character.
- MySQL's `ANSI_QUOTES` makes `"…"` a name.

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
- **`BEGIN`** at the start of a statement begins a transaction.

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
  protocol.
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
  of a write, such as `ANALYZE UPDATE …`, which runs it.
- A `WITH` (CTE) that changes data.
- A call to a function with side effects. Examples:
  `pg_terminate_backend`, `pg_cancel_backend`, `setval`, `nextval`,
  `set_config`, `pg_notify`, `dblink_exec`, `lo_import`, `lo_export`, the
  advisory locks, replication slots and origins, `pg_stat_reset`; MySQL
  `GET_LOCK`, `RELEASE_LOCK`, `SLEEP` and `BENCHMARK`; MariaDB's sequence
  functions `NEXTVAL`, `SETVAL` and `LASTVAL`, and `NEXT VALUE FOR`; SQLite
  `load_extension`. The full list per engine is in `classify.go`. Quoted
  and escaped function names are decoded first.
- `SELECT … FOR UPDATE` or `FOR SHARE` (MySQL: `LOCK IN SHARE MODE`), in a
  subquery or a `WITH` too. It locks its rows for as long as its result,
  or its transaction, is open.
- ClickHouse's `MOVE` (of users and roles between storages). PostgreSQL's
  `MOVE` only moves a cursor.
- On MySQL and MariaDB, what an executable comment (`/*! … */`,
  `/*!80000 … */`, MariaDB's `/*M! … */`) holds, and what it hides. A
  server older than the comment's version skips it, and MySQL skips
  MariaDB's, so the statement is read with each comment run and skipped,
  and the strictest reading counts. A statement whose comments name more
  versions than are read (`maxExecutableVersions` in `executable.go`)
  also asks first.

### Statements that change the server

These change the server rather than the session, so they count as writes:

- MySQL `RESET`, `SET GLOBAL`, `SET PERSIST`, `SET PASSWORD`,
  `SET DEFAULT ROLE` and `START REPLICA`.
- PostgreSQL `COMMIT PREPARED` and `ROLLBACK PREPARED`.
- DuckDB `SET`s of a setting that names a file it writes, such as
  `log_query_path`, `profiling_output`, `temp_directory`,
  `extension_directory`, `secret_directory` and `home_directory`.

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
- It never adds a space that would create a comment, such as between
  MySQL's `-` `-`.

## Tests

| Command | Checks |
| --- | --- |
| `go test ./internal/sqltext` | the lexer, splitter, classifier, parameters, completion and formatter |
| `go test ./internal/sqltext -run '^$' -fuzz '^FuzzTokenize$' -fuzztime 60s` | that tokens cover the text and formatting keeps every token |
| `DGOPHER_IT=1 go test ./internal/db -p 1 -run LexingConformance` | the lexing rules against the real servers |
| `go test ./internal/sqltext -run '^$' -bench Tokenize -benchmem` | the lexer's speed |
