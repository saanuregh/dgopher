# Where things are kept

Settings are JSON and scripts are `.sql`. What is yours alone in a project
(open editors, history, filters, row colours and the audit log) is in one
SQLite file, `.dgopher/state.sqlite`.

**A project folder** is yours to commit, except `.dgopher/`:

| Path | In Git | What |
| --- | --- | --- |
| `dgopher.json` | committed | connections without passwords, snippets, and the queries folder |
| `queries/**/*.sql` | committed | SQL files, each starting with `-- connection: <id>` |
| `.dgopher/.gitignore` | ignored | `*`: the folder ignores itself, so your `.gitignore` is never touched; written again on each open if it holds anything else |
| `.dgopher/state.sqlite` | ignored | the project's state (with SQLite's `-wal` and `-shm` files beside it), described below |
| `.dgopher/*.migrated` | ignored | the files an older DGopher kept this state in, set aside once imported; delete them when you like |
| `.dgopher/sample.sqlite` | ignored | the sample database, if you made it in this project |

`state.sqlite` holds, in its tables:
- `kv`: which query files were open, each one's connection and database,
  and the one in front, with the unsaved text of an editor whose file
  changed on disk (`workspace.json`); each table's recent grid filters
  (`filters.json`) and row colours (`colors.json`).
- `history`: query history (secrets redacted; trimmed to the newest 4,000
  once it passes 5,000; clearable).
- `audit`: the [audit log](audit-log.md), append-only and hash-chained.

Its schema has a version (`PRAGMA user_version`); a newer DGopher
upgrades it, and an older one refuses a file it does not know. On the first
start after the move to SQLite, the old files are imported in one
transaction; if one cannot be read, nothing is imported, the files stay as
they are, and the project says why.

A database file you put in the project yourself, such as `data/app.db`, is
yours to commit or ignore.

A project's state reopens only what is inside the project: editors,
dashboards and data models whose files are outside its folder (after `..`,
absolute paths and symbolic links are resolved; a link whose target is
missing counts as outside), or under its `.git/` or `.dgopher/`, are
skipped, so a cloned repository that ships its own `.dgopher/` cannot open
your other files. An editor you opened on a script outside the project does
not come back after a restart. A `.dgopher` that is a symbolic link stops
the project from opening. Keeping your text over a changed file outside the
project, or a link to one, asks first, showing its full path and where the
link points.

**The app's config directory** holds only what is about the app, not a
project: `~/.config/dgopher` on Linux, `~/Library/Application
Support/dgopher` on macOS, and `%AppData%\dgopher` on Windows.
`$DGOPHER_CONFIG_DIR` overrides it.

| File | What |
| --- | --- |
| `settings.json` | preferences, the project folders in the sidebar, and the connections you trusted, with what you agreed to (no secrets) to mark what a pull changes |
| `known_hosts` | SSH hosts trusted in the app (`~/.ssh/known_hosts` is also read) |

Trust stays here, not in the project, so that a cloned repository cannot
approve its own servers.

**The app's data directory** holds what the app keeps as it runs, apart
from your preferences: `$XDG_DATA_HOME/dgopher` on Linux
(`~/.local/share/dgopher` by default), `~/Library/Application
Support/dgopher` on macOS, and `%LocalAppData%\dgopher` on Windows.
`$DGOPHER_DATA_DIR` overrides it, and a set `$DGOPHER_CONFIG_DIR` stands for
it too, so that a run kept away from your config keeps away from your
layout as well.

| File | What |
| --- | --- |
| `ui.sqlite` | the windows' layout, a row a value in its `kv` table: the sidebar's width and whether it shows, the editor's height above its results (where new editors start), the split of two tabs side by side, and the Redis browser's keys width and console height |

Each value is written as a drag ends, and read at start; one missing keeps
its default. The window's own size and place are kept by the toolkit.
Without the file, as when it cannot be written, the layout lasts the run.

**Passwords** are never in either place: see
[Secrets](safety.md#secrets).
- A keychain password is kept for where it was typed for: the engine,
  host, port, user and database, the SSH tunnel, the Redis topology, and,
  when set, the client certificate, jump hosts, proxy, cloud identity and
  clear-text mode; a proxy's password for the proxy too. If a pulled
  `dgopher.json` points a connection somewhere else, or through a new proxy,
  the app finds no password for it and sends none, rather than send the
  stored one there; enter it in the connection's form. A password kept
  before these fields joined is asked for once, and can be kept again.
- Environment variables must be named `DGOPHER_*`, so a cloned repository
  cannot have the app send, say, `GITHUB_TOKEN` to a host of its choice. A
  password command is shown and asked for before it first runs, so a
  repository can have one print a variable only with your agreement.
- A connection you make or edit in the app is trusted as you save it.
  Before the first connect of one that arrived in `dgopher.json` from
  someone else, the app lists where it goes and how safely, and asks: the
  server, TLS and whether it is verified, the CA file, a client certificate,
  a proxy, SSH and jump hosts, where the password comes from (a variable, a
  command's exact text, or a cloud identity's command, which sends a token),
  clear-text passwords, the environment, read-only and the commit mode. It
  asks again when a pull changes any of these, marking what changed, or makes
  the connection protect less (production to staging or development,
  read-only off, manual commit to auto); a change that protects more is taken
  as it is.
