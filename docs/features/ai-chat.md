# AI chat

Keys are written as on macOS; on Linux and Windows, ⌘ is Ctrl and ⌥ is Alt.
[Keyboard shortcuts](../keyboard.md) lists the default keys.

![An editor with its AI chat beside it: a question about the statement at
the caret, a tool call reading the orders table, and SQL to insert in the
editor](../images/ai-chat.png)

Each SQL editor has an AI chat beside it: AI Chat in its bar, or ⌘K ⌘I,
which also hides the chat once it has the keys, as its × does. It keeps
the width you drag it to, whatever the window's. A new chat offers to
explain the statement at the caret, make it faster, say why the last run
failed, or write a query. With no editor in front,
⌘K ⌘I opens a new one on the connection in front, with its chat. The chat
talks to [Claude Code](https://claude.com/claude-code),
[Codex](https://github.com/openai/codex) or
[pi](https://github.com/earendil-works/pi), running the agent's own
command line, signed in as you, through
[agent-cli-connector](https://github.com/saanuregh/agent-cli-connector):
install `claude`, `codex` or `pi` first.

- **What it knows.** Each message carries what the editor shows: its
  connection, its file, the selection or else the statement at the caret
  with its lines, its text (up to 16 KB; only where it changed since the
  last message, to spare tokens), and the last run's error. A line
  under your message says what went, such as `query-1.sql · statement,
  line 3 · pg_prime`. "This query" and "the error" mean the editor's.
- **Only DGopher's tools.** The agent cannot read or change files, run
  shell commands, or browse: its own tools are turned off, and nothing
  from your or the project's agent settings loads (hooks, plugins,
  skills, extensions, MCP servers). Anything else it tries is refused
  without asking, with a line in the chat saying so. Codex, which has no
  switch for all of its tools, has its shell, patches, web search,
  plugins and the rest turned off one by one, runs in its read-only
  sandbox, and has every approval it asks for refused. It runs in the project folder of the editor's
  connection, else in a `chat` folder of the config directory, but cannot
  touch either.
- **SQL it writes** comes in SQL blocks, each with Insert, or Replace
  Selection while the editor has one, and New Editor. Insert puts the SQL
  in the editor as one change undo takes back; nothing runs until you run
  it. The lines it lands on are tinted a moment, the tint fading.
- **Connecting.** When a tool needs a connection that is not open, the
  chat asks: Connect, or Not Now. Connect opens it as DGopher always does,
  asking for a password where it needs one, and the tool goes on; calls
  waiting for the same connection share the one card. With Not Now, the
  agent answers without it.
- **The app's tools.** It lists the connections, lists a schema's tables
  and describes a table's columns, the editor's connection unless it names
  another. It runs a statement only as a read you allow, on a connection
  that is not production: one statement the safety policy reads as a read
  and would run without asking. Each such read shows its SQL and waits for
  Allow, Allow for This Chat (its reads of that connection, until the chat starts over) or
  Deny. Its rows, up to 200, go to the agent's provider. Every read is in
  the audit log; what is refused, too.
- **Charts.** Asked for a chart, it charts a read in the editor itself:
  a card shows the statement and the chart (kind, x column, series) and
  waits for Allow. Then the statement goes at the end of the script,
  selected, or in a new cell after the focused one, runs on the editor's
  session as a run of yours does, and its result opens in the Chart view
  as asked, where you change it as any chart. The same reads only, and
  never on production or while a transaction is open in the editor. The
  audit log records the run as the app's, for the chat. The first 10
  rows go to the agent, to describe the chart.
- **Its questions** show their choices as buttons.
- **Agent, model and effort** are chosen under the message field, which
  also shows what the next message carries of the editor. Switching
  agents starts over. The model picker lists the agent's models (pi's by
  provider) in a list of fixed height that scrolls, with a search field:
  every word typed matches a model's name, ID or provider; ↑ and ↓ move,
  ↵ chooses, Esc closes. The effort menu, the reasoning efforts the model takes. Both
  apply from the next message on and are kept, by agent, for the next
  chat.
- **While it works,** its orb livens and a ring of color flows around the
  message field; what it is doing shimmers. With the system's reduce
  motion setting, they hold still.
- **Replies** stream in as Markdown. Tool calls show on a line each, and
  open to their input and output; the agent's thinking opens the same way.
- ↵ sends and ⇧↵ starts a new line; what you send while it answers waits
  for the answer to end. Stop (⌘.) stops the answer, and New Chat starts
  over. While the chat has the keys, the editor's keys leave it alone, but
  ⌘S.
- **Conversations are kept.** Hiding the chat keeps its conversation, and
  so does quitting: an editor that comes back after a restart brings its
  chat back, open or hidden as it was, its messages read again from the
  agent's own store, and the next message continues the same session.
  Where the agent no longer has it, the next message starts a new one,
  and the chat says so. New Chat starts over; closing the editor ends its
  chat.
- **Keeping data from the agent.** A connection with **No rows to the AI
  chat** (Safety, in its form) shares its schema with the agent but no
  data: run_read_query refuses it, and a chart of it still draws in the
  editor while the agent hears only its columns and how many rows.
