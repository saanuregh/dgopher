# Troubleshooting

- [Installing and starting](#installing-and-starting)
- [Passwords and the keychain](#passwords-and-the-keychain)
- [Connecting](#connecting)
- [Projects](#projects)
- [Server activity](#server-activity)
- [Starting over](#starting-over)

For what DGopher does not support, see [Known limitations](limitations.md).
The app's own messages are in the [notifications](features.md#notifications)
list, and on standard error when it is started from a terminal
([Command line](command-line.md#logs)).

## Installing and starting

### macOS says the app cannot be opened

The builds are not signed yet. Open **System Settings → Privacy &
Security** and allow DGopher once. macOS 13 or later is needed.

### Windows SmartScreen warns about the installer

The builds are not signed yet. Choose **More info → Run anyway** once.

### Linux: the window does not open

DGopher draws its window with GTK 3 and OpenGL. The `.deb` package installs
what it needs. With the `.tar.gz`, install GTK 3 (`libgtk-3-0`), Pango,
Cairo, HarfBuzz, Fontconfig and libepoxy from your distribution.

## Passwords and the keychain

### "No system keychain is available"

DGopher keeps saved passwords in the system keychain: the macOS Keychain,
the Windows Credential Manager, or the Secret Service on Linux (GNOME
Keyring or KWallet). Without one, passwords are never saved and are asked
at each connect. **Settings → Storage** shows whether a keychain was
found.

On Linux, install and unlock GNOME Keyring or KWallet, then restart
DGopher. Or set the connection to read its password from a `DGOPHER_*`
environment variable or a command, such as a password manager's CLI
([Secrets](safety.md#where-passwords-come-from)).

### "Could not read the saved password from the keychain"

The keychain is locked, or it refused DGopher access. The connection does
not try without the password. Unlock the keychain (on Linux, GNOME Keyring
or KWallet), allow DGopher if the system asks, then **Retry**. The
connection's form shows the same note under the password; saving the form
with the field empty keeps the saved password.

### The password is asked again after a pull

A saved password belongs to where it was saved for: the host, port, user,
database, SSH tunnel, proxy and so on. If a pulled `dgopher.json` changed
any of these, the app sends no password to the new destination. Type it
once in the connection's form. See
[A password goes only where it was typed for](safety.md#a-password-goes-only-where-it-was-typed-for).

### "A connection may only read its password from a variable whose name starts with DGOPHER_"

Rename the variable, for example from `DB_PASSWORD` to
`DGOPHER_DB_PASSWORD`. The prefix keeps a cloned repository from making
the app send any other variable, such as `GITHUB_TOKEN`, to a server.

## Connecting

### "The SSH host key changed"

The SSH server presented a different key from the one in `known_hosts`.
Someone may be intercepting the connection, or the server may have been
reinstalled. Check with its administrator, then remove the old key from
`known_hosts` (DGopher's own, in the
[config directory](storage.md#the-config-directory), or `~/.ssh/known_hosts`)
before connecting again.

### "SSH tunnel closed"

The SSH tunnel under a connection ended: the network dropped, the SSH
server restarted or closed the connection, or it stopped answering
keepalives. The reason follows the message. The database sessions ended
with it, so an open transaction was rolled back. Click **Reconnect** once
the SSH server is reachable again.

### A cloud identity or MySQL clear-text password is refused

Both send a secret that an attacker in the path could reuse, so they go
only over verified TLS. Set TLS to **verify-full**, and give the server's
CA file unless the system already trusts it: AWS's RDS CA bundle, or Cloud
SQL's server CA.

### A PostgreSQL server that asks for a clear-text password fails under prefer or require

Neither mode checks who the server is, so the app refuses to send the
password. Use **verify-full**.

### "A client certificate needs TLS" or "needs both its file and its key's"

Set a TLS mode other than disable, and give both the certificate and its
key file.

### Kerberos on macOS finds no ticket

DGopher reads only a file credentials cache, while the macOS default cache
is not a file. Point `KRB5CCNAME` at a file cache and get a ticket into it:

```sh
export KRB5CCNAME=FILE:/tmp/krb5cc_$(id -u)
kinit -c "$KRB5CCNAME" you@EXAMPLE.COM
```

Then start DGopher from that shell.

### Backups: "pg_dump is not installed, or not on the PATH"

Backups and restores run the engine's own tools: `pg_dump` and
`pg_restore` (at least the server's version, and 16 or later for
verify-full without a CA file), `mysqldump` and `mysql`, SQL Server's
`sqlpackage` for BACPAC files, and Oracle's
Data Pump `expdp` and `impdp`, which come with Oracle's Instant Client
Tools package (installed with its Basic package). Install them and make
sure they are on the `PATH` DGopher starts with.

## Projects

### "The file changed on disk since it was read"

`dgopher.json`, a dashboard or a data model changed on disk, for example
after a `git pull`, since DGopher read it. DGopher never writes over such a
change. Reload the project from its menu in the sidebar, then make your
change again.

### A project shows an error and no connections

`dgopher.json` is not valid JSON, or a value is wrong. The error names the
problem. Fix the file, then reload the project. [Project files](project-files.md)
describes every field.

### A project will not open: `.dgopher` is a link

A `.dgopher` folder that is a symbolic link or a Windows junction is
refused, so a repository cannot point it at your other files. Replace it
with a real folder.

## Server activity

The **Statistics** page is empty until the server collects statement
statistics: install the `pg_stat_statements` extension on PostgreSQL,
enable `performance_schema` on MySQL, or turn on query logging on
ClickHouse. The page shows the steps for each.

## Starting over

To reset DGopher completely:

1. Quit DGopher.
2. Delete the [config directory](storage.md#the-config-directory) and the
   [data directory](storage.md#the-data-directory).
3. Delete the keychain entries whose service is `DGopher`.

Your projects are untouched. To reset only one project's own state (open
tabs, history, audit log), delete its `.dgopher/` folder; the audit log
goes with it.
