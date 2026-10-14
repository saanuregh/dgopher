# Command line and environment

## Starting DGopher

```sh
dgopher                 # start
dgopher <folder>        # start, and add the folder as a project
```

- The first argument, if it does not start with `-`, is added as a project
  folder. There are no flags; other arguments are ignored.
- A relative folder is resolved against the directory you ran the command
  in.
- **Only one DGopher runs at a time.** Running `dgopher` again brings the
  running app's window to the front and, with a folder, adds it there as a
  project. The second command then exits.
- If the folder cannot be added, the app says so and starts anyway.
- If DGopher cannot start, it prints `dgopher: <error>` and exits with
  status 1.

## Environment variables

| Variable | Effect |
| --- | --- |
| `DGOPHER_CONFIG_DIR` | Use this directory for settings, trusted connections and SSH hosts, and themes, instead of the [config directory](storage.md#the-config-directory). Also moves the data directory, unless `DGOPHER_DATA_DIR` is set. |
| `DGOPHER_DATA_DIR` | Use this directory for the windows' layout, instead of the [data directory](storage.md#the-data-directory). |
| `DGOPHER_*` | A connection set to read its password from an environment variable reads the one it names. The name must start with `DGOPHER_`. |
| `XDG_DATA_HOME` | On Linux, where the data directory goes, if it is an absolute path. |
| `LOCALAPPDATA` | On Windows, where the data directory goes. |
| `SSH_AUTH_SOCK` | The SSH agent that tunnels log in with. |
| `KRB5CCNAME` | The Kerberos credentials cache for PostgreSQL. Only a `FILE:` cache is read; without one, the default `/tmp/krb5cc_<uid>`. |
| `KRB5_CONFIG` | The Kerberos configuration file; `/etc/krb5.conf` by default. |

### Variables the app ignores on purpose

These would let the environment send a connection somewhere other than
where its form says:

- `PG*` variables, `~/.pgpass` and `~/.postgresql`'s certificates are
  never read for PostgreSQL connections.
- `PG*`, `MYSQL_*` and `LIBMYSQL_*` variables are removed before the app
  runs `pg_dump`, `pg_restore`, `mysqldump` or `mysql` for a backup or
  restore.
- `HTTP_PROXY` and `HTTPS_PROXY` are ignored by ClickHouse over HTTP. Set
  a proxy in the connection's form instead.

## Logs

DGopher writes its own log messages, such as a theme file that could not be
read, to standard error. Start it from a terminal to see them. These are
separate from the [audit log](audit-log.md).

## Running a development copy

To keep a test run away from your real settings, layout and trusted
connections:

```sh
DGOPHER_CONFIG_DIR=$(mktemp -d) dgopher
```
