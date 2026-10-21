# Keyboard shortcuts

These are the default keys. Keys are written as on macOS; on Linux and
Windows, ⌘ is Ctrl and ⌥ is Alt. Two keys in a row, as ⌘K →, are pressed
one after the other: the title bar shows the first while it waits for the
second, and Esc forgets it.

- F1 lists every shortcut as currently set, and finds one by its key or
  what it does.
- **Customize Keyboard Shortcuts…** (in Settings, the Help menu or the
  palette) changes any of them, and records two keys in a row.
- **Open keymap.json** (⌘K ⌘S) opens the file the keys you set are kept
  in, as Zed keeps them: see [Your keymap file](#your-keymap-file).

The defaults are defined in
[`internal/keymap/commands.go`](../internal/keymap/commands.go), each
action by the name keymap.json gives it, as `editor::Run`.

## Everywhere

| Key | Does |
| --- | --- |
| ⇧⌘P | Command palette: every command, connection, table and snippet |
| ⌘P | Open a table or a query file by name |
| ⌘T | New SQL editor on the current connection |
| ⌘K ⌘T | New SQL notebook on the current connection |
| ⌘K ⌘I | The editor's AI chat: show it, or hide it |
| ⌘N | New connection |
| ⌘O | Open a SQL script |
| ⌘W | Close the tab |
| ⇧⌘T | Reopen the tab closed last |
| ⌘1…⌘8 | Go to tab 1 to 8 of the group |
| ⌘9 | Go to the last tab of the group |
| Ctrl+Tab, Ctrl+⇧Tab | Next and previous tab of the group |
| ⌘Y | Query history |
| ⌘⇧A | Audit log |
| ⌘K ⌘E | Show the latest error |
| ⌘⇧N | Notifications: every error and message of this session |
| ⌘, | Settings |
| ⌘K ⌘S | Open keymap.json |
| ⌘B | Show or hide the sidebar |
| F11 | Enter or leave full screen |
| ⌘0 | Focus the navigator |
| ⌘L | Focus the filter of the current view: a table's `WHERE`, then its quick filter |
| F1 | The list of keys |
| ⇧F10, Menu key | Open the context menu of what has focus |
| ⌘Q | Quit, asking first about anything it would lose |
| (none) | Add an existing folder as a project |

In a dialog, ⌘↵ presses the main button from any field, and Esc closes the
dialog.

Keys marked *fixed* below cannot be changed.

## Editor groups

| Key | Does |
| --- | --- |
| ⌘K →, ⌘\ | Split right: the tab in a new group beside |
| ⌘K ←, ⌘K ↑, ⌘K ↓ | Split left, up or down |
| ⌘K ⇧→, ⌘K ⇧←, ⌘K ⇧↑, ⌘K ⇧↓ | Move the tab to the group on that side, made when there is none |
| ⌘K ⌘→, ⌘K ⌘←, ⌘K ⌘↑, ⌘K ⌘↓ | Focus the group on that side |
| ⌘K ⌘1…⌘K ⌘9 | Focus group 1 to 9 |
| ⌘K ⌘W | Close the group: its tabs move to the group beside |
| (none) | Focus the next group; join every group into one |

## Navigator

| Key | Does |
| --- | --- |
| ⌘↵ | New editor with `SELECT *` of the table |
| ⌘R | Read the schema again |
| ⌘C | Copy the table's name, quoted |
| F2 | Rename the table |
| F4 | The table's structure |
| Delete | A `DROP` of the table, in a new editor to review and run; on a query file, dashboard or data model, delete it |
| ↑, ↓ (fixed) | Move; type a name's first letters to jump to it |
| →, ← (fixed) | Open or close |
| ↵ (fixed) | Connect, or open a table's data |

## SQL editor

| Key | Does |
| --- | --- |
| ⌘↵, F5 | Run the statement at the caret, or the selection |
| ⌘⇧↵, ⌥X | Run the whole script; in a notebook, the whole cell |
| ⌘⌥\ | Run into a new result tab, keeping the results shown |
| ⌘E | Explain the statement |
| ⌘⇧E | Explain Analyze: run the statement and measure it |
| ⌘⌥⇧↵ | Commit the open transaction |
| (none) | Roll back the open transaction; set a key if you want one |
| ⌘J | Show or hide the results |
| ⌘⇧J | Switch between the editor and its results |
| (none) | Maximize the results over the editor, or restore them |
| ⌃Space | Complete tables, columns and keywords |
| ⌘⇧F | Format the SQL |
| ⌘F, ⌘⌥F | Find, and replace |
| ⌘⇧O | Go to a statement of the script |
| F12, ⌘-click | Go to the definition of the name at the caret |
| ⌘I | Show the card of the name at the caret |
| ⇧F12 | Find the usages of the name at the caret |
| F2 | Rename the name at the caret, everywhere in the file |
| ⌥↵ | Fix the problem at the caret |
| ⌘⌥↑, ⌘⌥↓ | Add a caret on the line above or below |
| ⌘D | Select the word, then its next occurrence, with a caret at each |
| ⌘⇧L | Select every occurrence of the word, with a caret at each |
| ⌘⌥[, ⌘⌥] | Fold or unfold the block at the caret |
| ⌘⌥⇧[, ⌘⌥⇧] | Fold or unfold every block |
| ⌘/ | Comment or uncomment the lines with `--` |
| ⌘⌥/ | Put the selection in a `/* … */` block comment, or take it out |
| ⌥↑, ⌥↓ | Move the line up or down |
| ⌥⇧↑, ⌥⇧↓ | Duplicate the line up or down |
| ⌘⇧K | Delete the line |
| ⌃G | Go to a line |
| Esc (fixed) | Cancel the running statement |
| ⌘S | Save now (editors also save as you type) |
| ⌘⇧S | Save as a file |
| ⌘K ⌘N | Show the editor as a notebook of cells, or as one script |

## Notebook

A notebook's cells are editors: the SQL editor's keys work in them too.

| Key | Does |
| --- | --- |
| ⇧↵ | Run the cell and go to the next, made when there is none |
| ⌘⌥↵ | Run the cell, staying in it |
| ⌘K ⌘↵ | Run every cell, from the first |
| ⌘K ⌘A, ⌘K ⌘B | Add a SQL cell above or below |
| ⌘K ⌘D | Delete the cell |
| ⌥⇧↑, ⌥⇧↓ | Move the cell up or down |
| ⌘K ⌘Y, ⌘K ⌘M | Change the cell to SQL or to Markdown |
| ⌥PageUp, ⌥PageDown | Go to the cell above or below |
| (none) | Run the cells above, or this one and those below; clear every cell's results |

## AI chat

| Key | Does |
| --- | --- |
| ↵ (fixed) | Send the message; ⇧↵ starts a new line |
| ⌘. | Stop the reply |

## Results grid

| Key | Does |
| --- | --- |
| ⌘C | Copy the cell, or the rows chosen |
| ⌘⇧C | Advanced Copy: delimiter, column names, quoting, row numbers |
| ⌘V | Paste over the chosen cell, as pending changes |
| ⌘⇧V | Advanced Paste: delimiter, header row, as new rows |
| Arrows (fixed) | Move between cells; with ⇧, choose the cells between |
| ↵, F2 (fixed) | Edit the cell |
| Esc (fixed) | Leave the cursor's cell alone chosen; with one cell, revert its change |
| ⇧↵ | Open the value editor |
| Tab (fixed) | Record view of the chosen row |
| ⌘` | Next view: grid, record, text |
| F7 | Show or hide the panels |
| ⌘F11 | The column's distinct values, to filter by |
| ⌘⌥S | Order by the chosen column: ascending, descending, none |
| ⌘G | Go to a row by number |
| ⌘⇧G | Go to a column by name |
| ⌘↑, ⌘↓ | First and last row |
| ⌘⇧↑, ⌘⇧↓ | Choose the rows up to the first, or down to the last |
| ⌥⇧Space | Choose the whole rows of the chosen cells |
| ⌘⇧Space | Choose the whole columns of the chosen cells |
| ⌘A (fixed) | Choose every cell |
| ⌥Space | Follow the cell's foreign key |
| ⌥↓ | Choose the cell's value from the table its foreign key points at |
| ⌥Insert | Add a row |
| ⌘⌥Insert | Duplicate the chosen row |
| Delete, ⌘⌫, ⌥Delete | Mark the chosen rows for deletion |
| ⌘D, ⌘⌥D | Copy the value of the row above or below |
| ⌘Z, ⌘⇧Z | Undo and redo a pending change |
| ⌘S | Review and apply the pending changes |
| ⌘R | Discard the pending changes; without any, read the rows again |
| F5 | Read the rows again |
| ⌘⌥N | Fetch the next page of rows |
| ⌘⇧= | Fetch every row |
| (none) | Show or hide the chart of the rows |

## Other views

| View | Key | Does |
| --- | --- | --- |
| Redis key tree | F5 | Scan the keys again |
| Redis key tree | Delete | Delete the chosen key, once you agree |
| Redis console | ↑, ↓ | Walk the command history |
| Redis console | Tab | Complete the command's name |
| Dashboard | F5 | Run the panels again |
| ER diagram | ↵ | Open the chosen table's data |
| ER diagram | Arrows | Move the chosen table |

## Your keymap file

The keys you set are kept in `keymap.json` in the
[config directory](storage.md), over the defaults, as
[Zed's](https://zed.dev/docs/key-bindings) are: a list of blocks, each the
keys of a context. The app reads it again as soon as it changes on disk,
and says what of it does not read.

```json
// // and /* */ comments and trailing commas are allowed.
[
  {
    // Workspace is anywhere; Navigator, Editor, Notebook, Grid, Chat and
    // Dialog are where their keys work.
    "context": "Editor",
    "bindings": {
      "cmd-k cmd-f": "editor::Format",   // two keys in a row
      "f5": null,                        // the key does nothing there
      "alt-1": ["workspace::ActivatePane", 1]
    }
  }
]
```

- **Keys** are written as `cmd-shift-f`: `cmd` is ⌘ on macOS and Ctrl
  elsewhere, so that a file works on both; `ctrl`, `alt`, `shift` and
  `super` are those keys everywhere. Two keys in a row are apart by a
  space. A key that types, as a letter, needs ⌘, Ctrl or Alt, also as the
  second of two.
- **Actions** are named `namespace::Name`; the keys editor in Settings
  shows each one's name. One that takes a number is written
  `["pane::ActivateItem", 3]`. `null` takes a key from whatever the
  defaults gave it, in that context.
- **Contexts.** A key of a narrower context wins over the same key of
  Workspace while that context has the focus. An action of Workspace may
  be bound in a narrower context, where it then works only.
- The first of two keys cannot also be a key on its own where both work.
- The keys editor writes the same file, without its comments.
- Keys an older DGopher kept in `settings.json` move to `keymap.json` the
  first time this version runs.
