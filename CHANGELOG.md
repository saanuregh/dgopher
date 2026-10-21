# Changelog

The changes of each version of DGopher.

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
