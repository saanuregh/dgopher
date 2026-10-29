# Getting started

- **Try the sample.** The start page offers a small shop database in
  SQLite, made on your computer, so you can explore without a server.
- **The tour.** A short guided tour explains projects and connections,
  editors and results, the palette, how production connections are
  guarded, and the sample database. It highlights the part of the window
  each step is about. The start page offers it until you take it; the
  Help menu and the palette always do.
- **Paste a connection URL** into the connection form to fill it in. See
  [Connection URLs](connections.md#connection-urls).
- **Drag and drop** a file onto the window:

  | File | What happens |
  | --- | --- |
  | SQLite (`.sqlite`, `.sqlite3`, `.db`, `.db3`) or DuckDB (`.duckdb`, `.ddb`) | opens as a connection |
  | `.sql` | opens in an editor on the connection chosen in the navigator |
  | CSV, TSV, Parquet, JSON or JSON Lines (`.csv`, `.tsv`, `.parquet`, `.json`, `.jsonl`, `.ndjson`) | queried in place with DuckDB |
  | Excel (`.xlsx`) or XML | imported into a new table of the connection chosen in the navigator |

- **Open Recent** in the File menu (and the Dock, the jump list on Windows,
  and recent files on Linux) lists the last SQL, SQLite and DuckDB files
  opened, which open as a dropped file does.
- **Offline.** While the computer is offline, the bar of an unconnected
  editor says so. Redis subscriptions and MONITOR that stopped start again
  once it is back online; nothing else runs again by itself.

## Projects

Every connection belongs to a project: a folder, such as a repository.
The sidebar lists every project with its connections, query files,
dashboards and data models. [Where things are kept](../storage.md) lists the
files.

- **Create** one with **New Project…**, or add an existing folder with
  **Add Existing Folder…** or `dgopher <folder>`.
- **Share with your team.** `dgopher.json` holds the project's connections
  (with their environments) and snippets, and no secrets. It is written
  sorted and stable, so it diffs cleanly.
- **Never overwritten.** A file changed on disk, as by a `git pull`, is
  never overwritten. Reload the project from its menu.

### Query files

- Every SQL editor is a file in `queries/`, saved as you type. Its tab
  shows a dot until the file holds what you typed.
- A `-- connection: <id>` line binds a file to a connection. Connection IDs
  complete as you type the line. A `-- view: notebook` line in the first
  lines opens the file as a notebook; without it, it opens as a script.
- A file runs only on the connection its line names. An editor whose file
  names another connection runs nothing and offers to switch to it. A file
  naming a connection the project lacks opens unconnected, so you can fix
  its line. A restored editor follows its file's line, as after a pull that
  changed it.
- A file changed on disk while open reloads at once when the editor holds
  no unsaved text; otherwise it is never overwritten, and the editor offers
  **Reload** or **Keep Mine**. A file deleted on disk is marked so.
- The sidebar's list of query files follows the folder as files come and go.
- An editor closed without typing anything removes its file.
- **Duplicate** (in an editor tab's menu, a query file's menu, or the
  palette) opens a copy of the editor, including unsaved edits, in a new
  query file on the same connection.
- Query files, dashboards and data models are renamed (F2) or deleted
  (Delete) from their menu in the sidebar; an open tab follows. A
  dashboard's or a model's file is named after it.

### Connection IDs

A connection's ID changes in its form, once you agree to what that
rewrites:

- `dgopher.json` and its tables' settings;
- the query files, dashboards and data models that name it;
- open editors, keeping their unsaved text;
- its saved password and trust, which move with it.

Query history and the audit log keep the old ID. Teammates who pull the
change are asked again whether to trust the connection, and for its
password. The button beside the ID makes a new one from the name, numbered
when another connection in the project has it.
