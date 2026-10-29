# Moving data

## Import files

Import a CSV, TSV, text, JSON, JSON Lines, Parquet, Excel or XML file into
a table (**Import Data…**), or into a new table made from the file's
columns (**Import File as New Table…** on a schema, or drop an Excel or XML
file on the window).

- **Reading:** DuckDB reads the file, finding a CSV file's delimiter and
  header and every format's column types. You can change the delimiter, the
  header and the sheet, read every column as text, and choose an XML file's
  rows by their element. Nested JSON values become JSON text, and exact
  numbers keep their digits.
- **Columns:** into an existing table, the file's columns map to the table's
  by name. Into a new table, each kept column takes a name and a type of the
  engine, and the `CREATE TABLE` shows before it runs.
- **All or nothing:** the import runs in one transaction, in `INSERT`s of
  many rows. A failing row imports nothing, and a table made for the import
  is not left behind. It waits for an open transaction on the database to
  end first.
- **ClickHouse** has no transactions, so the rows before a failing one stay
  in an existing table, and the error says how many.

Limits, so a bad file cannot exhaust memory:

- A file holds at most 16,384 columns (Excel's A to XFD).
- An Excel sheet holds at most 1,048,576 rows, and a cell at most 1 MB of
  text. The workbook's shared strings hold at most 4,194,304 items and
  128 MB of text, in a part of at most 256 MB unpacked; its other parts,
  except sheets, are at most 64 MB unpacked.
- In a workbook or an XML file, elements nest at most 10,000 deep, with at
  most 10,000 namespaces declared by the elements open at once. A tag (or
  comment, CDATA section or other markup) holds at most 1 MB and 16,384
  attributes, and a run of text between tags at most 1 MB.
- An XML file is read twice, first for its columns and then for its rows,
  so its rows are never all held at once.
- A file past a limit, or one that cannot be read, says so; it does not
  stop the app.

## Copy tables

**Copy to Another Database…** on a table (or **Copy Tables to Another
Database…** on a schema) copies to a database of the same engine or
another.

- A table that is not there is created with the same columns, nullability
  and primary key, with types that hold the values exactly (on the same
  engine, the same types).
- A table that is there stops the copy, gets the rows added, or has its rows
  replaced, as you choose.
- Each table copies in one transaction where the target supports it,
  through the target's safety policy, and is audited.

## Compare rows

**Compare Rows with…** on a table compares its rows with another table's,
in the same database or another, of any engine.

- Rows match by the target's primary key, over the columns both have.
- Values compare as values: `1.50` and `1.5` are equal, as are `1` and
  `true`, a time and the same time in UTC, and JSON with its keys in another
  order.
- It lists rows found in only one table and the columns changed, hiding the
  values the grid hides.
- It can then make the target's rows match the source's: adding, updating,
  and deleting only when you ask. This runs in one transaction through the
  target's safety policy, each statement changing exactly one row, audited
  with hidden values masked. Changes without hidden values can also open as
  SQL, to read or edit first.

## Generate test data

**Generate Test Data…** on a table adds up to 1,000,000 rows.

- Each column's values are made as its type and name suggest: names,
  e-mails, phone numbers, cities, companies, addresses, URLs, words and
  paragraphs; numbers, decimals, dates and times in a range; booleans,
  UUIDs and JSON; a choice of values (an enum's by default); an existing
  value of the table a foreign key refers to; or the column's default, as
  for auto-increment keys. Each column's settings can change this.
- Keys continue from the highest. Unique text differs from earlier runs'
  too. Text fits its column. A nullable column can take a share of NULLs.
- A sample shows first. The rows are added in one transaction where the
  engine supports it, through the safety policy, and audited.

## Back up and restore

**Back Up…** and **Restore…** on a connection or a database:

| Engine | Backup |
| --- | --- |
| PostgreSQL | `pg_dump`, as an archive or SQL; schema, data or both |
| MySQL | `mysqldump`, in one transaction, with routines, triggers and events |
| SQLite | a copy with `VACUUM INTO`, while the database stays in use |
| DuckDB | a folder of Parquet files (`EXPORT DATABASE`) |
| SQL Server | a BACPAC file on this computer (its schema and rows), which `sqlpackage` exports and imports; or a copy-only `BACKUP DATABASE` into a file on the server, which `RESTORE DATABASE` restores over the database ([engines](../engines.md#sql-server)) |
| Oracle | Data Pump: `expdp` exports a schema (its definitions, its rows or both) into a dump file that stays on the server, which `impdp` imports ([engines](../engines.md#oracle)) |

- The tools reach the server as the connection does, through its tunnel or
  proxy. Their password goes in their environment or a file of their own,
  never on their command line. Their progress shows as they write.
- The tools run without your environment's variables that begin with
  `PG`, `MYSQL_` or `LIBMYSQL_`; the rest reach them. A database's name is
  only a name (one holding `=` or a URL changes nothing). `pg_dump` and
  `pg_restore` never read `~/.pgpass` or `PG*` variables, and `mysqldump`
  reads no option file but the app's. Oracle's `expdp` and `impdp` read
  `ORACLE_HOME` and `TNS_ADMIN` from the environment, as their wallets and
  network files need.
- Under verify-full without a CA file, the PostgreSQL tools trust the
  system's roots (they need version 16 or later). `mysqldump` needs the
  connection's CA file. Through a tunnel or proxy, `mysqldump` checks the
  certificate against the CA but not the server's name, and the dialog says
  so.
- A `pg_dump` archive restores with `pg_restore` in one transaction,
  dropping what it creates first if you ask. A SQL file restores as Run SQL
  File runs it.
- A DuckDB folder is imported into a separate DuckDB that can reach only
  that folder, then copied into the database: tables, views, macros, types,
  sequences, indexes and keys, all or nothing. A view that reads a file
  outside the folder cannot be restored. The restore waits for an open
  transaction on the database to end.
- A restore always asks once, with the connection's name typed on
  production (for a SQL file, once it has been read through). Backups and
  restores are both audited.

## Run SQL File

**Run SQL File…** (in the File menu and on connections and schemas) runs a
file of SQL, such as a dump from `pg_dump`, `mysqldump` or `sqlite3`,
without opening it in an editor. Files of any size work; each statement can
be up to 64 MB and each `COPY` row up to 1 GB.

1. **Read.** It reads the file through first and says what it holds: how
   many statements of each kind, each one that destroys data (by line), and
   each one that commits a transaction the file began (such as MySQL's DDL).
   The safety policy asks once for all of it; on production, a file that
   writes asks for the connection's name.
2. **Run.** It reads the file again as it runs it, on a session of its own:
   in one transaction, all or nothing, where the database supports one and
   the file has no transactions of its own; otherwise statement by
   statement, stopping at the first error or continuing, as you choose. A
   lost connection always stops it, because the rest would run without what
   the file had set. A file changed in between is not run.
3. **Clean up.** A file that ends, stops or fails with a transaction it
   began still open is rolled back, the rollback audited, and the run fails:
   "BEGIN at line N was never committed; its changes were rolled back". On
   MySQL the server says whether a transaction is open, so a dump that turns
   autocommit off and commits ends well.

A PostgreSQL dump's `COPY … FROM stdin` rows are loaded, and its
`\restrict` lines skipped. Other psql commands, such as `\connect`, stop
the run, because only psql can run them. MySQL's `DELIMITER` and executable
comments work as in the `mysql` client.
