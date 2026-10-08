# Where things are kept

Settings are JSON and scripts are `.sql`. What is yours alone in a project
(open editors, history, filters, row colours and the audit log) is in one
SQLite file, `.dgopher/state.sqlite`.

**A project folder** is yours to commit, except `.dgopher/`:

| Path | In Git | What |
| --- | --- | --- |
| `dgopher.json` | committed | connections without passwords, snippets, and the queries folder |
| `queries/**/*.sql` | committed | SQL files, each starting with `-- connection: <id>` |
| `.dgopher/.gitignore` | ignored | `*`: the folder ignores itself, so your `.gitignore` is never touched |
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

**The app's config directory** holds only what is about the app, not a
project: `~/.config/dgopher` on Linux, `~/Library/Application
Support/dgopher` on macOS, and `%AppData%\dgopher` on Windows.
`$DGOPHER_CONFIG_DIR` overrides it.

| File | What |
| --- | --- |
| `settings.json` | preferences, the project folders in the sidebar, and the connections you trusted |
| `known_hosts` | SSH hosts trusted in the app (`~/.ssh/known_hosts` is also read) |

Trust stays here, not in the project, so that a cloned repository cannot
approve its own servers.

**Passwords** are never in either place: see
[Secrets](safety.md#secrets).
- A keychain password is kept for where it was typed for: the engine,
  host, port, user and database, and the SSH tunnel. If a pulled `dgopher.json` points a connection somewhere
  else, the app asks again rather than send the stored password there.
- Environment variables must be named `DGOPHER_*`, so a cloned repository
  cannot have the app send, say, `GITHUB_TOKEN` to a host of its choice.
- A connection you make or edit in the app is trusted as you save it.
  Before the first connect of one that arrived in `dgopher.json` from
  someone else, and whenever a pull changes where it goes (engine, host,
  port, user, database or tunnel), how safely (TLS mode or CA file), or
  where its password comes from (variable or command), the app shows the
  destination and the exact command, and asks.
