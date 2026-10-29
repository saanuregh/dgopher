# Results grid

Keys are written as on macOS; on Linux and Windows, ⌘ is Ctrl and ⌥ is Alt.
[Keyboard shortcuts](../keyboard.md) lists the default keys.

One viewer shows both a query's results and a table's data.

## Viewing

- Only the rows in view are built, so large results stay fast. A result
  keeps at most 200,000 rows.
- Number columns and their headers line up on the right, digit under
  digit; other columns start at the left. The text view does the same, as
  psql does.
- Columns resize, move, sort by several at once, pin to the left, hide (or
  hide those with no data), and fit to their values or the screen, from the
  header's menu.
- Three views: grid, record (Tab), and plain text (``⌘` `` cycles them).
- Formats: thousands separators, dates, booleans, binary and NULL, and
  numbers and dates in your region's format (which keeps every digit); row
  colors by value; zoom, also with ⌘= and ⌘-, ⌘K ⌘0 for the default size,
  ⌘ with the mouse wheel and pinching a touchpad.
- **Refresh**, **Fetch All** (with Stop, which keeps the rows read), and on
  a table **Count** (`SELECT COUNT(*)`, with Stop). The keys: F5, ⌘⇧= and
  ⌘⌥N for the next page.
- Move with ⌘G (go to a row), ⌘⇧G (go to a column), and ⌘↑ and ⌘↓ (first and
  last row).
- Alt+click, or **Show Card** in the menu, on a header or a cell shows its
  [card](sql-editor.md#hover-cards): the value whole, as JSON, a time or the row a
  foreign key points at where it is one.

## Choosing cells

- **A block of cells**: drag over the cells, or click one and Shift-click
  another, or extend with ⇧ and the arrows. ⌘-click adds a row or takes it
  out; ⌘⇧↑ and ⌘⇧↓ extend to the first and last row.
- **Whole rows**: click or drag down the row numbers, or press ⌥⇧Space.
  **Whole columns**: Select Column in a header's menu, or ⌘⇧Space. The
  corner above the row numbers, or ⌘A, chooses everything.
- A right-click acts on the cell it is on: outside the chosen cells it
  chooses that cell first. On a row number, it chooses the whole row, and
  the menu leaves out what acts on one column (Edit Value, Filter, Order).
- Esc leaves the cursor's cell alone chosen.
- The cell menu's **Select** submenu also chooses the rows matching the
  cell's value, the rows with pending changes, or the rows not chosen.
- The status bar counts the chosen rows and columns, with the count, sum,
  average, minimum and maximum of their numbers. While a drag chooses cells,
  it and the Calc panel keep what they showed, and follow the new block when
  the drag ends.

## Filtering and sorting

- **Filter by a cell**: =, ≠, >, <, contains, NULL, or the values of the
  rows chosen (`IN` or `NOT IN`);
  also by a typed value, the clipboard's value, or a list of distinct values
  with counts (⌘F11). The header menu removes a column's filters or clears
  them all.
- **The `WHERE` filter** is SQL that runs on the server, with recent
  filters and column names offered as you type. On a table it is the
  table's `WHERE`. On a query's result it wraps the statement
  (`SELECT * FROM (…) q WHERE …`), with its parameter values. ⌘L focuses it.
- **The filter builder**, beside the `WHERE` box, writes that filter from
  conditions you choose: a column, an operator (=, ≠, >, ≥, <, ≤, contains,
  does not contain, starts with, ends with, in a list, not in a list,
  between, is NULL, is not NULL) and a value, matching all or any. The box
  then shows the SQL.
- **The quick filter** narrows the rows already read without running
  anything.
- **Sorting** runs on the server while rows remain to be read, and in memory
  once all are read. ⌘2 cycles the chosen column's order.
- A statement that writes is never filtered, and refreshing its result asks
  before running it again.
- A query's result lists distinct values and groups on the editor's own
  session, so it sees the editor's schema, open transaction and temporary
  tables. While a result is still reading rows, these wait for Fetch All.

## Panels

F7 shows the panels. Each can be maximized.

| Panel | Shows |
| --- | --- |
| Value | the chosen value as text, JSON, XML, hex or an image; save it to a file or load it from one |
| Calc | count, distinct, NULLs, sum, average, min, max, median and the most frequent value of the chosen cells, for each chosen column |
| Profile | every column at once: NULLs, distinct values, bounds, average and median, with a histogram of numbers and times or the most frequent text values |
| Metadata | the result's columns and types |
| Grouping | counts by the chosen columns, in memory or on the server |
| References | the rows of other tables that refer to the chosen row |

Profile works on the rows read at once, or on every row on the server when
asked. DuckDB summarizes a table itself (`SUMMARIZE`); the other engines
count in one statement.

## Copy and paste

- **Copy** (⌘C) the cell or the chosen cells, in the columns shown; also
  with the header, or the column names alone. Hidden columns are never
  copied.
- **Advanced Copy** (⌘⇧C): choose the delimiter, column names, quoting and
  row numbers.
- **Copy as** CSV, TSV, JSON, SQL, Markdown or a text table, a column's
  values as `name IN (…)`, or the values as a list (`1, 'a', NULL`).
  Plain-text copies hold up to 100,000 rows.
- **Paste** (⌘V) over the chosen cell as pending changes. **Advanced Paste**
  (⌘⇧V) chooses the delimiter and header row, or pastes as new rows.
- Open the rows in a spreadsheet app.
- **Generate SQL** for the chosen rows (`SELECT`, `INSERT`, `UPDATE`,
  `DELETE` with their values), or for the table (with an upsert, joins, and
  `:name` parameters for the values, and its DDL).

## Editing rows

- Change cells, add, duplicate and delete rows, set NULL or DEFAULT (where
  the column has one), revert a row, and undo and redo (⌘Z, ⌘⇧Z), with
  DBeaver's keys. Set to NULL, Set to DEFAULT and Revert act on every chosen
  cell, as one change to undo. ⌘D and ⌘⌥D copy the value from the row above or below.
- ⌘S shows the changes as SQL to review, then applies them in one
  transaction, one row per statement by primary key. See
  [Grid edits](../safety.md#grid-edits).
- **Edit Value…** (⇧↵) edits a cell in a form that suits its type, beside its
  text: a colored tree for JSON, as its card shows it (a click on a key
  copies the expression reading it), a list of an enum's values (read from
  PostgreSQL's catalog, or from the type elsewhere), a calendar and time for
  dates, and a switch for booleans.
- **Foreign keys.** Choose from… in a cell's menu (or ⌥↓) lists the rows of
  the table the key points at, by key with their first columns, searchable
  by key or text. ⌥Space follows the key to the row it points at; the
  Navigate menu also lists the rows of other tables that refer here.
- **Virtual keys.** A table without a primary key becomes editable with a
  virtual key, kept in `dgopher.json` for the team. It works in the table's
  tab and in query results.
- ClickHouse rows cannot be edited in the grid.

### When a query's result is editable

A query's result can be edited when:

- its statement only reads (not one that calls a function such as
  `nextval`, nor `FOR UPDATE`);
- it is one `SELECT` from one table: no `WITH`, join, grouping, aggregate,
  `DISTINCT`, `UNION`, window function, subquery or table function in
  `FROM`;
- its columns include the table's key.

Computed and renamed columns stay read-only. The status line says why a
result cannot be edited.

Its edits apply on the editor's session. So with manual commit, or MySQL's
autocommit off, they join the editor's open transaction, or open one and
leave it open. On DuckDB, which has no savepoints, they wait until an open
transaction ends. In an editor, ⌘S reviews the edits, then saves the file.

## Sensitive values

Columns with names such as `password`, `secret`, `token`, `api_key`,
`private_key`, `salt`, `otp`, `ssn`, `tax_id`, `card_number`, `cvv`, `iban`
or `pin` (in any case style, `camelCase` too) show `••••••`:

- in the grid and the value panel;
- in the figures of the Profile, Calc and Grouping panels;
- in the review and the audit entry of edits.

A column's header menu hides or shows any column's values. For a table's
rows, this choice is kept in `dgopher.json` for the team. A setting
(Settings → Safety) shows the sensitive ones. Copies and exports keep the
real values.

## Comparing results

Pin a result's rows, then compare another result with them, from the
grid's compare button. Rows match by the table's key, a column both have,
or their position. The comparison shows rows that differ, A above B with
changed cells marked, and rows found in only one.

## Table tabs

- **Data**, ordered by primary key by default, with paging.
- **Structure**: columns, indexes and foreign keys, with links to the
  referenced tables. **Edit Structure** opens the table form (see
  [Schema tools](schema-tools.md)).
- **DDL**: the table's definition.
- **Diagram**: the tables that refer to this one (on the left) and those it
  refers to (on the right).

## Export

Export a result, a table, or several tables at once.

- **Formats:** CSV, TSV, JSON, JSON Lines, SQL, Markdown, an Excel
  workbook, Parquet, or a DuckDB database.
- **Rows:** the rows already read, or the query run again (only a read runs
  again), up to a row limit or without one. The limit is kept from one
  export to the next (1,000,000 at first). Reaching the limit is always
  reported.
- **Session:** a query result's export runs on the editor's session, so it
  sees the same schema, temporary tables and transaction as the rows shown.
  A table's export runs on a session of its own.
- **File names** come from a pattern with `${table}`, `${connection}`,
  `${timestamp}` and `${date}`. The folder can open after the export.
- **Options:** the delimiter, the text for NULL, and the rows per `INSERT`.
  Text formats can go to the clipboard instead.
- **CSV and TSV** can start with a byte order mark, for Excel, and can guard
  against spreadsheet formulas (off at first, then as last chosen). Text
  starting with `=`, `+`, `-`, `@`, a tab or a line break (or their
  full-width forms) gets a leading `'`, so a spreadsheet shows it instead of
  running it. Numbers are never changed, nor are clipboard copies.
- **SQL** names its table from the dialog's Table and Schema fields, quoted
  as the engine quotes names. The schema (a MySQL or ClickHouse database) is
  filled in for a table, and empty for a query's rows. An empty schema
  writes the table's name alone.
- **Markdown** escapes `|`, `\`, `&` and `<`.
- **Excel** keeps numbers, dates and booleans as such, with a bold header
  that stays in view. A number longer than the 15 digits Excel keeps, such
  as a large ID, stays text, so it is never rounded. Rows, columns or text
  beyond Excel's limits stop the export rather than write a file Excel
  would have to repair.
- **Export Tables…**, on a schema, writes each chosen table or view to its
  own file named by the pattern, with the same formats and a limit for
  each. Its SQL `INSERT`s name each table with its schema.
- Every export is recorded in the [audit log](../audit-log.md).
