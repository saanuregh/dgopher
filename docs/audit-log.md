# Audit log

Every action on a database is appended to the `audit` table of
`.dgopher/state.sqlite` in its project. Open the current project's log with ⌘⇧A, from the command
palette, or from a project's menu in the sidebar.

**What it records:**
- connects and disconnects;
- every statement run (reads included);
- a SQL file run without an editor: the file's path and SHA-256, how many
  statements ran and how many rows they loaded, and each of its
  statements apart from INSERT and COPY, and each that failed;
- the SQL generated from grid edits, and their outcome;
- imports, exports, cancelled queries and ended sessions;
- Redis commands;
- each confirmation the safety policy asked for, and whether the
  connection's name was typed;
- what the policy refused;
- trusted SSH hosts, and connections added, changed or deleted.

**Each entry holds** the time, the OS user and host, the connection, its
environment, rows and duration, and the error if any; a connect also
records the server's version and whether TLS was used. A failing password
or identity command is recorded as failed with its exit status, without
what it printed.

**Tamper evidence:**
- Every entry carries the SHA-256 of the entry before it, and a number.
- **Verify Integrity** walks the whole chain, in the order written. It
  names the first entry that was changed, removed, inserted or moved, and
  it notices a log whose first entries are missing.
- What a hash chain alone cannot show: entries cut off the end, or a log
  rewritten whole by someone who can write its files. For those, **Copy
  Head** gives the last entry's number and hash. Keep it somewhere else,
  such as a ticket or a change record. Compared later, it shows whether the
  log was rewritten or cut short.
- The log is never trimmed or cleared by the app.
- Each entry is written in one SQLite transaction that reads the last
  entry first, so two DGopher windows on one project keep one chain, and
  a crash cannot leave half an entry.
- A log an older version kept in `audit*.jsonl` files is imported as it
  was: a chain broken there stays broken, at the same entry. A damaged end
  of the last file, from a write cut short, is left out, and an entry says
  so; the files are kept as `*.migrated`.
- Secrets (passwords in `CREATE`/`ALTER USER`, the arguments of functions
  and engines that take credentials, URL and connection-string passwords,
  Redis `AUTH`, `CONFIG SET requirepass` and similar; see
  [How statements are read](safety.md)) are redacted before they are
  written.
- **Export** saves the whole log to a file of your choice, one JSON entry
  per line, oldest first. The exported file carries the same chain, so it
  can be checked on its own.
