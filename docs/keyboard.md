# Keyboard shortcuts

These are the default keys. Keys are written as on macOS; on Linux and
Windows, ⌘ is Ctrl and ⌥ is Alt.

- F1 lists every shortcut as currently set, and finds one by its key or
  what it does.
- **Customize Keyboard Shortcuts…** (in Settings, the Help menu or the
  palette) changes any of them. See
  [Keyboard shortcuts](features.md#keyboard-shortcuts) in Features.

The defaults are defined in
[`internal/keymap/commands.go`](../internal/keymap/commands.go).

## Everywhere

| Key | Does |
| --- | --- |
| ⌘K | Command palette: every command, connection, table and snippet |
| ⌘P | Open a table or a query file by name |
| ⌘T | New SQL editor on the current connection |
| ⌘N | New connection |
| ⌘O | Open a SQL script |
| ⌘W | Close the tab |
| ⇧⌘T | Reopen the tab closed last |
| ⌘1…⌘9 | Go to tab 1 to 9; ⌘9 is the last tab. While a results grid has focus, ⌘2 sorts instead. |
| Ctrl+Tab, Ctrl+⇧Tab | Next and previous tab |
| ⌘Y | Query history |
| ⌘⇧A | Audit log |
| ⌘, | Settings |
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
| ⌘⇧↵, ⌥X | Run the whole script |
| ⌘\ | Run into a new result tab, keeping the results shown |
| ⌘E | Explain the statement |
| ⌘⇧E | Explain Analyze: run the statement and measure it |
| ⌘⌥⇧↵ | Commit the open transaction |
| (none) | Roll back the open transaction; set a key if you want one |
| ⌘J | Switch between the editor and its results |
| ⌃Space | Complete tables, columns and keywords |
| ⌘⇧F | Format the SQL |
| ⌘F, ⌘⌥F | Find, and replace |
| ⌘⇧O | Go to a statement of the script |
| F12, ⌘-click | Go to the definition of the name at the caret |
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
| ⌘2 | Order by the chosen column: ascending, descending, none |
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
