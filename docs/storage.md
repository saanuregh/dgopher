# Where things are kept

DGopher keeps data in three places:

| Place | Holds | Shared? |
| --- | --- | --- |
| [The project folder](#the-project-folder) | connections, query files, dashboards, data models, and your own state for the project | yes, through Git, except `.dgopher/` |
| [The config directory](#the-config-directory) | your preferences, trusted connections and SSH hosts | no |
| [The data directory](#the-data-directory) | the windows' layout | no |

Passwords are in none of them. See [Secrets](safety.md#secrets).

Settings are JSON, and scripts are `.sql` files.

## The project folder

A project is a folder, such as a repository. Commit everything in it
except `.dgopher/`:

| Path | In Git | What |
| --- | --- | --- |
| `dgopher.json` | committed | connections (without passwords), snippets, per-table settings such as virtual keys and hidden columns, and the queries folder |
| `queries/**/*.sql` | committed | SQL files, each starting with `-- connection: <id>` |
| `dashboards/` | committed | dashboards |
| `models/` | committed | data models |
| `.dgopher/.gitignore` | ignored | holds `*`, so the folder ignores itself and your own `.gitignore` is never touched |
| `.dgopher/state.sqlite` | ignored | your state for this project (below), with SQLite's `-wal` and `-shm` files beside it |
| `.dgopher/sample.sqlite` | ignored | the sample database, if you created it in this project |
| `.dgopher/*.migrated` | ignored | files an older DGopher kept this state in, set aside once imported; delete them when you like |

A database file you put in the project yourself, such as `data/app.db`, is
yours to commit or ignore. SQLite and DuckDB files inside the project are
stored by relative path, so they open in every clone.

The app writes `.dgopher/.gitignore` again on each open if it holds
anything other than `*`, or is not a regular file.

### `state.sqlite`

This file holds what is yours alone in a project:

| Table | What |
| --- | --- |
| `kv` | small values, listed below |
| `history` | query history, with secrets redacted. Trimmed to the newest 4,000 entries once it passes 5,000. You can clear it. Each entry links to its audit entry. |
| `audit` | the [audit log](audit-log.md): append-only and hash-chained. It records every statement and command sent, so it grows with use. It is never trimmed. |

The `kv` table holds:

| Key | What |
| --- | --- |
| `workspace.json` | the open tabs, in order, and the one in front: query files (with each one's connection, database, and unsaved text if its file changed on disk), dashboards, data models, and a connection's tabs by what they show, such as a table's schema, name and page |
| `filters.json` | each table's recent grid filters |
| `colors.json` | each table's row colors |
| `query-parameters.json` | the editors' parameter values, per connection |
| `dashboard-parameters.json` | the dashboards' parameter values, per dashboard |

The file's schema has a version (`PRAGMA user_version`). A newer DGopher
upgrades it, and an older one refuses a file it does not know.

When a project from before this file is first opened, its old files are
imported in one transaction. If one cannot be read, nothing is imported,
the files stay as they are, and the project says why.

### What a project may reopen

A project's state reopens only what is inside the project. This stops a
cloned repository that ships its own `.dgopher/` from opening your other
files.

- Editors, dashboards and data models whose files are outside the project
  folder are skipped. Paths are checked after resolving `..`, absolute
  paths and symbolic links; a link whose target is missing counts as
  outside.
- Files under the project's `.git/` or `.dgopher/` are skipped too.
- An editor you opened on a script outside the project does not come back
  after a restart.
- A `.dgopher` that is a symbolic link, or a Windows junction, stops the
  project from opening.
- Keeping your text over a changed file outside the project, or over a link
  to one, asks first and shows its full path and where the link points.

## The config directory

This holds what is about you and the app, not about one project.

| System | Path |
| --- | --- |
| Linux | `~/.config/dgopher` |
| macOS | `~/Library/Application Support/dgopher` |
| Windows | `%AppData%\dgopher` |

Set `DGOPHER_CONFIG_DIR` to use another directory.

| File | What |
| --- | --- |
| `settings.json` | preferences, the project folders in the sidebar, your custom keys, and the connections you trusted (no secrets) |
| `known_hosts` | SSH hosts trusted in the app; `~/.ssh/known_hosts` is also read |
| `themes/` | your theme files (Settings → Appearance → Themes Folder) |

For each trusted connection, `settings.json` keeps what you agreed to, so
the app can show what a later pull changes. Trust stays here, not in the
project, so a cloned repository cannot approve its own servers.

## The data directory

This holds what the app keeps as it runs, apart from your preferences.

| System | Path |
| --- | --- |
| Linux | `$XDG_DATA_HOME/dgopher` (by default `~/.local/share/dgopher`) |
| macOS | `~/Library/Application Support/dgopher` |
| Windows | `%LocalAppData%\dgopher` |

Set `DGOPHER_DATA_DIR` to use another directory. If only
`DGOPHER_CONFIG_DIR` is set, it is also used as the data directory, so a
run kept away from your config also keeps away from your layout.

| File | What |
| --- | --- |
| `ui.sqlite` | the windows' layout |

`ui.sqlite` has two tables:

- `windows`: each window's size, position, and whether it is maximized or
  full screen.
- `kv`: one row per layout value: the sidebar's width and whether it shows,
  the editor's share of the height above its results (where new editors
  start), whether the results are hidden or maximized, the split of two
  tabs side by side, and the Redis browser's key list width and console
  height.

Each value is written when a drag ends and read at start. A missing value
keeps its default. If the file cannot be written, the layout lasts only
for the run.
