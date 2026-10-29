# SQL editor

Keys are written as on macOS; on Linux and Windows, ⌘ is Ctrl and ⌥ is Alt.
[Keyboard shortcuts](../keyboard.md) lists the default keys.

## Editing

- Syntax highlighting for each engine's dialect, and line numbers. Only the
  lines in view are drawn, so a long script scrolls and edits as fast as a
  short one.
- Brackets and quotes close as you type them, and wrap a selection. Enter
  keeps the indentation and continues a `--` comment.
- Move lines with ⌥↑ and ⌥↓, duplicate them with ⌥⇧↑ and ⌥⇧↓, and delete
  them with ⌘⇧K. ⌃G goes to a line.
- ⌘/ comments or uncomments the lines with `--`, and ⌘⌥/ puts the
  selection in a `/* … */` block comment or takes it out.
- **Several carets.** ⌘⌥↑ and ⌘⌥↓ add one on the line above or below;
  ⌥-click adds one where you click (and removes one you click on). ⌥⇧-drag
  makes a column selection. ⌘D selects the word at the caret, then its next
  whole occurrence; ⌘⇧L selects every one. What you type, delete or
  complete at one caret happens at each. The arrows, Home and End move them
  all; Esc goes back to one.
- **Folding.** A statement, a group in parentheses or a block comment over
  several lines folds to its first line and a ⋯. Use the gutter's markers,
  or ⌘⌥[ and ⌘⌥] at the caret (add ⇧ for every block). Folded text stays in
  the file, and runs.
- **Find and replace** (⌘F, ⌘⌥F): match case, whole words or a regular
  expression, in the selection or the whole text, one match at a time or
  all at once. ⌘Z undoes a Replace All.
- **Format** (⌘⇧F) formats the selection, or the whole text, keeping the
  blank lines between statements.
- **Right-click menu**: cut, copy, paste, comments, case, folding and
  selections; then run, format, copy the statement, save as a snippet, and
  export from the query.

## Finding your way

- **Problems are underlined as you type**: a string, quoted name or comment
  left open; a parenthesis that closes nothing or is not closed; and,
  against the catalog, a table or a column (`alias.column`) that does not
  exist. The line under the editor says what is wrong at the caret, with
  the closest existing name as a fix that ⌥↵ applies. Statements that
  create or drop tables are not checked for names.
- **Go to Definition** (F12, or ⌘-click) opens what the name at the caret
  refers to: a table's or view's structure (also through an alias or one of
  its columns), or a routine's definition. With ⌘ held, the names the
  catalog knows underline under the pointer, so a name that stays plain is
  one it does not know.
- **Hover cards** tell what a name is without leaving the query: see
  [Hover cards](#hover-cards).
- **Find Usages** (⇧F12) lists where the name is used in this file and the
  project's other query files.
- **Rename in File** (F2) renames the name everywhere in the file, quoting
  it where needed. (The navigator's Rename renames it in the database.)
- **Go to Statement** (⌘⇧O) lists the file's statements by line, with the
  comment above each, filtered as you type.

## Completion

- Completes tables, columns (aware of aliases), schemas, keywords, the
  engine's common functions and the schema's own functions (with their
  parentheses), and snippets by their keyword.
- The list opens as you type, or with ⌃Space. It narrows as you type, best
  match first, matching the letters in order (`usr` finds `users`). ↵ or
  Tab inserts the chosen item.
- A table completed after `FROM` or `JOIN` gets a short alias from its
  words' first letters (`reflex_order ro`), kept apart from the statement's
  other aliases and from keywords (`ro2`).

## Running statements

- **Statements end** at `;` or at a blank line, so ⌘↵ runs the statement
  around the caret without selecting it; a band shows which statement. A
  setting makes `;` the only end. A procedure, function or trigger body
  (`BEGIN … END`, `BEGIN ATOMIC`) stays one statement, and MySQL
  `DELIMITER` lines work as in the `mysql` client.
  [How SQL is read](../sql.md#splitting-a-script) has the details.
- **Run** the statement (⌘↵ or F5), the selection, or the whole script (⌘⇧↵
  or ⌥X). `⌘\` runs into a new result tab, keeping the others. Result tabs
  can be pinned.
- **A result tab is named for what ran:** the table it reads, else the
  statement's first words, numbered when repeated. Hovering it shows the
  statement, and the ▶ of the statement whose result shows is marked in the
  accent color, until that statement is edited.
- **The results** sit under the editor and keep their share of its height as
  the window resizes. ⌘J hides them down to their row of result tabs and
  shows them again, as VS Code's panel; a run shows them. The buttons at the
  right of that row hide them and maximize them over the editor. Every
  editor shares the choice, and it is kept for the next start.
- **A ▶ in the gutter**, on the first line of each statement, runs that
  statement. It is faint but on the statement a run would send, green, and
  on the one pointed at.
- **On an error,** a script stops or continues, as a setting says. A failed
  statement shows the database's message, code, detail, hint, and where it
  went wrong; **Go to Error** puts the caret there.
- **Statement timeout**: optional, per connection.
- **Each editor has its own session**, so `SET`, temporary tables and
  transactions last between statements.
- **Commit and roll back** from the editor's bar in manual commit, or with
  ⌘⌥⇧↵ to commit. Roll back has no key unless you set one, because it loses
  the transaction's work.
- **Schema and database.** The editor's toolbar shows the schema it looks
  up names in (and on PostgreSQL its database), and switches them. A schema
  switch runs `SET search_path` or `USE` on its session, and is audited. A
  database switch uses a new session, and never happens with a transaction
  open. Typing a `SET` or `USE` moves the toolbar too.

## Parameters

- `:name` parameters (also `@name` and `$name` on SQLite), ClickHouse
  `{name:Type}` parameters and `${var}` variables take their values from
  the **Parameters bar** above the editor, which lists those in the text.
- A run asks only for values left empty there.
- Values are kept for the connection's editors, for you alone, across
  restarts.
- All but `${var}` are sent separately from the SQL. **Output** lists the
  values a statement ran with.
- A statement that mixes `:name` with `$1` or `?` placeholders is refused.

See [How SQL is read](../sql.md#parameters).

## Query plans

- **Explain** (⌘E) shows the plan. **Explain Analyze** (⌘⇧E) runs a
  statement that reads and measures it, on PostgreSQL, MySQL, SQL Server,
  Oracle and DuckDB.
- The plan shows as a tree of steps, with rows estimated and found and each
  step's share of the time or cost; as a flame graph; or as the server's
  raw rows.
- **Advice** above the plan points at what may make the query slow: a scan
  reading every row to keep few, estimates far from what happened, a sort or
  hash spilling to disk, a nested loop rescanning a table, or a ClickHouse
  key that narrows nothing.
- A typed `EXPLAIN` in one of these forms shows the same way.

![The plan of a join on PostgreSQL, run by Explain Analyze: each step with
the rows estimated and found, and its share of the time](../images/query-plan.png)

## Notebooks

![A notebook: a Markdown cell titling it, and two SQL cells, each with
its run count and its own result](../images/notebook.png)

- **Notebook view.** The Notebook button, ⌘K ⌘N or the palette shows an
  editor as cells, as Jupyter does; Script shows it as one editor again.
  New SQL Notebook (⌘K ⌘T, in the File menu and the palette) opens a new
  query file as a notebook of one cell.
- **Cells** are SQL, each its own editor with completion, cards, problems
  and Vim keys, or Markdown, shown as it reads, and edited with a click on
  a focused cell. Each SQL cell shows its results under it: rows in a grid,
  plans, errors, and what statements without rows did. Its `[n]` numbers
  its last run, and what it took shows under it.
- **One session.** Every cell runs on the editor's session, as a
  notebook's cells run on one kernel: a temporary table, a `SET` or a
  transaction one cell begins, the next sees. Runs go through the same
  safety policy, parameters, confirmations and audit log as the editor's.
- **Running.** ⇧↵ runs the cell and goes to the next, making one after the
  last; ⌘⌥↵ runs it and stays; ⌘↵ runs the statement at the caret, and
  ⌘⇧↵ the whole cell. Run All (⌘K ⌘↵) runs the cells in order and stops at
  an error, unless scripts skip errors (Settings → Editor); Esc or Stop
  stops it. A thin line of light sweeps along the top of the cell
  running, so a run is followed from cell to cell; [*] marks it and […]
  those waiting.
- **Cell actions,** shown on the focused cell or the one pointed at: move
  up or down, change to SQL or Markdown, delete. ⌘K ⌘A and ⌘K ⌘B add a
  cell above or below; Clear empties every cell's results.
- **The file stays SQL.** A cell begins with a line `-- %%`, and a
  Markdown cell with `-- %% [markdown]`, its lines commented, as
  [jupytext's percent format](https://jupytext.readthedocs.io/en/latest/formats-scripts.html)
  writes them, so the file runs as a script and diffs as text. A file
  opens as a script, cells or not, unless its header has a
  `-- view: notebook` line. Switching to the notebook view writes that
  line under the `-- connection:` line, and switching back takes it out,
  so the file opens as it was left, for everyone who shares it. A script
  without cell lines shows a cell per statement, and its cells are
  written only once one is edited. Results are not kept
  in the file: it is shared, and they may hold what should not be.

## Files and sessions

- Editors come back on the next launch, as scripts or notebooks as they
  showed. `.sql` files can be opened (⌘O) and
  saved (⌘S, or ⌘⇧S to save as).
- A restored editor, or an opened file, does not connect on its own. Press
  Connect, or run a statement.

## Vim keys

Turn on in **Settings → Editor → Vim**.

- Normal, insert, visual and visual line modes, with a block caret and the
  mode shown under the editor.
- Counts.
- Motions: `h j k l w b e W B E 0 ^ $ gg G f F t T ; , % { } + -`, Enter,
  Ctrl+D and Ctrl+U.
- Operators `d c y > <` with motions, or doubled; text objects
  `iw aw iW aW i" a" i' a' i` a` i( a( ib ab i[ a[ i{ a{ iB aB`.
- `x X D C Y s S r J ~ p P o O i a I A`, `u` and Ctrl+R, and `.` to repeat
  the last change, typing included.
- Search with `/ ? n N`. `:w` saves and `:N` goes to line N.
- The register is the clipboard.
- The app's own commands keep their keys (such as ⌘↵). While the editor
  has the keys, Vim's Control keys win over the app's. Outside insert mode,
  keys Vim does not use (such as Page Up or ⌘F) act as in the editor.

## Hover cards

Rest the pointer on a name for a moment and a card tells what it is. In
the navigator and diagrams, while one card shows the next opens at once,
so a list can be read by moving down it. The pointer can go into a card,
to click its buttons or select its text; it closes a moment after the
pointer leaves it, and with Escape. The pin keeps a card open, in front,
until closed.

A card also opens without the wait: **Alt+click** a table of the navigator,
a cell or a header of a grid, or a connector of a diagram, or choose
**Show Card** in its menu; the editor's is ⌘I at the caret. A card opened
so stays until Escape or a click outside it.

- **In the SQL editor** (or ⌘I at the caret):
  - a table or a view: its row estimate, size, comment, columns (keys and
    foreign keys first, those the statement uses marked) and the tables it
    joins. Clicking a column inserts it at the caret, by the table's alias;
    clicking a joined table writes the `JOIN … ON …` after the statement's
    FROM clause and joins.
  - an alias: the table it stands for; a column, written `t.column` or alone
    when one table has it: its type, constraints, foreign key and comment.
  - `*`: the columns it reads, and **Expand** writes them out.
  - a CTE or a subquery's alias: the columns it gives.
  - a function: its signatures, as the server describes its built-in
    functions (PostgreSQL, DuckDB and ClickHouse) or from a table kept with
    the app (SQLite, MySQL and MariaDB); a routine of the schema: its
    arguments, and its definition.
  - a parameter: the value the next run sends.
  - Keywords have no card. A problem at the name shows first, with its fix.
- **In the navigator:** a table's or a view's card, with **New Query**.
- **In grids**, only when asked for, as the pointer crosses cells all the
  time: a column header shows its column; a cell shows its value whole,
  with its column, and more where the value has it:
  - JSON: the document indented and colored, its objects and arrays
    folding; clicking a key copies the expression reading it, as the
    engine writes it (`payload->'items'->0->>'sku'` on PostgreSQL,
    `payload->>'$.items[0].sku'` on MySQL, SQLite and DuckDB,
    `JSONExtractString(payload, 'items', 1, 'sku')` on ClickHouse).
  - A foreign key: the row it points at, read once and kept, its sensitive
    values hidden. On production it is read only when you click
    **Show Row**. The read is recorded in the history like any other.
  - A time: in your zone, in UTC, how long ago, and as Unix time; also for
    whole numbers in a column named as a time, such as `created_at`.
  - A long or cut text: the whole of it.
  - A date kept as text, as SQLite keeps them, in a column named as one
    (`created_at`, `order_date`): read as a time.
  - A masked value has no card, which would show what the grid hides.
- **In ER diagrams:** a connector's foreign key.

Settings → Appearance → Hover cards turns the cards that open on a rest
off, everywhere or in the editor, the navigator or diagrams, and sets how
long the pointer rests first.

## History and snippets

### History

One dialog per project, with two tabs:

- **Queries** (⌘Y) lists every statement and command run from an editor or
  the console, with its timing and error. ↵ opens one again in a new editor
  on its connection; you can also jump to its entry in the audit log.
  Clearing the queries leaves the audit log as it is.
- **Audit Log** (⌘⇧A) is the [audit log](../audit-log.md).

### Snippets

- Save the selected SQL as a snippet with a name and a keyword (**Save as
  Snippet…**). Insert it from the palette, or by typing its keyword in an
  editor.
- Built in: `sel`, `selc`, `ins`, `upd`, `del` and `join`.
- A snippet's fields, written `${1:default}` and `$2`, are filled in one
  after another with Tab; `$0` marks where the caret ends.
- A project's snippets are kept in `dgopher.json`, shared with the team.
