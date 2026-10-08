# How SQL is read

Everything the app does with SQL text (highlighting, finding the statement
at the caret, splitting a script, the safety policy, parameters, completion
and formatting) starts from one lexer in
[`internal/sqltext`](../internal/sqltext). It never fails: half-typed SQL
in the editor still produces tokens that cover the whole text.

A wrong rule here can do more than color a word wrongly. If the app ends a
statement where the server does not, a `DROP` can reach the server inside a
statement the [safety policy](safety.md) read as a `SELECT`. The rules
below therefore follow each server, and a test checks them against the
real servers.

## Dialects

| Engine | Lexer dialect |
| --- | --- |
| PostgreSQL | `Postgres` |
| DuckDB | `Postgres` (its parser derives from PostgreSQL's) |
| MySQL | `MySQL` |
| ClickHouse | `ClickHouse` |
| SQLite | `SQLite` |

The mapping is `safety.Dialect` in
[`internal/safety`](../internal/safety/safety.go).

## Lexing rules that move statement boundaries

| | PostgreSQL, DuckDB | MySQL | ClickHouse | SQLite |
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

In every dialect a character outside ASCII belongs to a name, and only
ASCII spaces, tabs and line breaks separate words: every server reads them
this way. A multi-character operator never takes in the start of a
comment, so `//* c */` keeps its comment.

[`internal/db/lexing_conformance_test.go`](../internal/db/lexing_conformance_test.go)
runs a query per rule on each server and checks that the lexer agrees.

## Splitting a script

A statement ends at `;`, or at a line holding only whitespace unless a
setting keeps `;` only. A blank line inside parentheses or a `CASE … END`
does not end one.

- **Routine bodies.** In `CREATE PROCEDURE`, `FUNCTION`, `TRIGGER` or
  `EVENT`, a `BEGIN … END` body (and PostgreSQL's `BEGIN ATOMIC`) keeps its
  `;`s. If the body never closes, the text splits at every `;` instead, so
  an unfinished body can never hide the statements after it. `BEGIN` and
  `END` used as names do not open or close a body.
- **MySQL `DELIMITER`.** A line `DELIMITER //` sets the delimiter to its
  first word, as the `mysql` client does; the line itself is not sent.
  `DELIMITER ;` restores the default.
- A statement-initial `BEGIN` starts a transaction.

## Classification

Each statement gets a class (read, write, schema change, transaction or
session), its verb, and whether it is destructive; the
[safety policy](safety.md) decides from these. The rules are in
[`classify.go`](../internal/sqltext/classify.go).

- **Unknown verbs** count as writes.
- **More than one statement** in a text the policy receives counts as a
  destructive write. On PostgreSQL the server refuses such text too:
  statements without parameters run through the extended protocol.
- **A routine's text keeps the danger of what it holds**: creating a
  procedure whose body has a `DROP` is confirmed as the `DROP` would be.
- **Writes hidden in reads**: `INTO` a table or file after `SELECT`,
  `TABLE` or `VALUES`; `EXPLAIN ANALYZE` and `DESC ANALYZE` of a write;
  data-modifying CTEs; calls to functions with side effects, listed per
  engine in `classify.go`, with quoted and escaped names decoded first.
- **Server-level statements** count as writes: MySQL `RESET`,
  `SET GLOBAL`, `SET PERSIST`, `SET PASSWORD`, `SET DEFAULT ROLE`,
  `START REPLICA`, and PostgreSQL `COMMIT PREPARED`.
- **Reads** include `SHOW`, `DESCRIBE`, `EXPLAIN`, `FETCH`, cursor
  declarations over a query, DuckDB `SUMMARIZE`, `PIVOT` and `FROM`-first
  queries, `CHECK TABLE`, and the SQLite and DuckDB `PRAGMA`s that only
  report (`table_info`, `index_list` and similar). Any `PRAGMA` with `=`
  is a write.

## Parameters

| Syntax | Engines | Sent as |
| --- | --- | --- |
| `:name` | all | a placeholder with its value |
| `@name`, `$name` | SQLite | a placeholder with its value |
| `{name:Type}` | ClickHouse | a server-side query parameter; the text is unchanged |
| `${var}` | all | written into the text, then reviewed again |

A statement that mixes `:name` with `$1` or `?` placeholders is refused.
The code is [`internal/sqltext/params.go`](../internal/sqltext/params.go)
and [`internal/params`](../internal/params/params.go).

## Formatting

The formatter changes only whitespace and the case of keywords; every other
token keeps its text and order. ClickHouse and MySQL keep names in the case
they were written, so there only clause, join and operator words are
upper-cased. It never adds a space that would make a comment, such as
between MySQL's `-` `-`.

## Tests

- `go test ./internal/sqltext` covers the lexer, splitter, classifier,
  parameters, completion and formatter.
- `FuzzTokenize` checks that tokens cover the text and that formatting
  keeps every token:
  `go test ./internal/sqltext -run '^$' -fuzz '^FuzzTokenize$' -fuzztime 60s`.
- `DGOPHER_IT=1 go test ./internal/db -p 1 -run LexingConformance` checks the
  rules against the servers.
- `go test ./internal/sqltext -run '^$' -bench Tokenize -benchmem` measures
  the lexer.
