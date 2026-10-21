# Changelog

The changes of each version of DGopher.

## 0.4.0

### Added

- Errors say what most likely went wrong: a rejected password, an unknown
  database, nothing listening, a host not found, a timeout, a TLS or SSH
  problem, a keychain that could not be read. The full text is under
  Details, with Copy, and the fix is beside it: Retry, Edit Connection,
  Reconnect or Refresh.
- Notifications: a bell in the title bar lists the session's errors,
  messages and log lines. Show Latest Error (⌘K ⌘E) and Notifications
  (⌘⇧N) open them from the keyboard.
- An SSH tunnel that ends after connecting fails its connection with the
  reason and offers Reconnect; open transactions' rollbacks are audited.

### Changed

- What you do that fails tells you at once in a dialog: a connect you asked
  for, a lost SSH tunnel, part of the navigator you opened. Connects at
  start and by restored tabs stay quiet and go to the notifications.
- Errors wait their turn ("1 of 2") instead of replacing each other, and
  every brief message shows.
- A failed connection says why once, in the editor's toolbar; the navigator
  marks it with ⚠, and its error rows take the keyboard's selection.
- A table's Structure says "Couldn't read indexes" or "Couldn't read foreign
  keys" rather than none.

### Fixed

- A keychain that could not be read no longer lets saving the connection
  form delete the saved password.
- A connect that asked for a password, or to trust an SSH host, now shows
  its failure.

## 0.3.0

### Added

- Tabs: drag a tab out past the window's edge to open it in a new window.
- Windows: the window's minimize, maximize and close buttons are drawn in
  DGopher's theme, with Windows 11's snap layouts on maximize.

### Changed

- Faster drawing: only what changed is redrawn and laid out again, windows
  out of sight draw nothing, and mouse-wheel scrolling glides (MyGo 0.6).
- Titles beside smaller text line up on a shared baseline.
- Linux: the Debian package and install script no longer require WebKitGTK.

### Fixed

- A file dropped on the middle of the window now opens.

## 0.2.1

### Added

- Updates: DGopher checks for a new version once a day and offers to
  install it, from Check for Updates… in the app menu or the title bar's
  menu. Updates are signed, and the app installs only those signed with
  DGopher's key. The Debian package does not update itself.

## 0.2.0

### Added

- Notebooks: a SQL file whose header says `-- view: notebook` shows as
  cells, each of which runs on its own and keeps its own results.
- Editor groups: split the editor by dragging a tab or with a key. Each
  group has its own tabs, and a status bar for its connection.
- Keymaps: Zed-style key bindings, with two-key sequences, set in
  `keymap.json`.
- AI chat beside an editor, on Claude Code, Codex or pi, with a choice of
  model and effort. The agent cannot write files or run a shell: it lists
  and describes tables, runs read queries and draws charts, each after
  asking you, through the safety checks and into the audit log.
