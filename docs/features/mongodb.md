# MongoDB

Browse and edit a MongoDB database's documents, and run MongoDB shell
statements in a console. FerretDB works as this page says, without what
it lacks ([engines](../engines.md#ferretdb)). DBeaver Community Edition
has no MongoDB support; this page follows the MongoDB shell (`mongosh`)
and DGopher's own SQL tabs instead.

## Connecting

- Paste a `mongodb://` or `mongodb+srv://` URL, or fill in the form: host
  and port, more hosts, DNS SRV, replica set, auth source, auth
  mechanism, read preference and direct connection.
- TLS, SSH tunnels and proxies work as for the other engines, except
  that a DNS SRV connection can use neither a tunnel nor a proxy: the
  record is looked up on this machine. For DNS SRV, the `authSource` the
  record's TXT entry names outranks the database, as in `mongosh`. See
  [Engine support](../engines.md#mongodb) for the rules and the auth
  mechanisms.

## Navigator

- A connection lists its databases, each with its size on disk. A
  database lists **Collections** and **Views** folders; a time-series or
  capped collection is marked so, and a view says which collection it is
  on. A collection lists its indexes, unique ones marked, with their keys
  and options in the tooltip.
- The menus have **Open Documents** (on a collection or view), **Open
  Console** and **Refresh**, which reads that part again.

## Collection browser

- **Filter, sort and fields** bars take the shell's value syntax:
  `{ status: "paid", total: { $gt: 100 } }`, with unquoted keys,
  `ObjectId()`, `ISODate()` and the other helpers the console accepts. A
  mistake is shown with its place before anything is sent. **Find** (↵)
  runs them; **Refresh** reads the documents again.
- **Grid, JSON and Tree** views of the same documents. The grid has a
  column per top-level field, with its type; nested documents and arrays
  show compact; a double click or ⇧↵ on one opens the document beside
  the rows, as a tree or as JSON to edit.
- Documents load 200 at a time, with **Load More** and **Fetch All**.
- **Edits wait until you save**, as in the SQL grid: edit or remove a
  top-level field from its cell, edit a whole document as JSON beside the
  rows (**Keep Changes**, or **Revert** one document), **Insert…** a
  document (one without an `_id` gets a new ObjectId), **Duplicate** one,
  or **Delete** the chosen ones. Changed and new documents are marked,
  deleted ones tinted red, and the bar counts them. A document's `_id`
  cannot change: duplicate it and delete the old one. Views and
  read-only connections cannot be edited, and say why.
- **Save…** shows the statements it will run, one per edit, and asks
  once through the [safety policy](../safety.md#mongodb). **Show
  Statements** shows them without saving. An edit saves as `updateOne`
  with `$set` and `$unset`. When a changed field's name holds a dot or
  starts with `$`, which those operators read as a path or an operator,
  the update is a pipeline that sets that field by its name with
  `$setField` (MongoDB 5.0 and later; FerretDB refuses it), so the
  fields the edit does not name stay as they are. It says whether
  the save is one transaction (all or nothing) or, on a server without
  transactions, applies one by one and keeps the edits before a failure.
  Each edit must match exactly one document by its `_id`; one changed or
  deleted since it was read fails the save. **Stop** stops before the
  next edit. While a save runs, Find, Refresh, Discard, Load More and
  Fetch All are refused; while documents are read, edits and Save are.
  **Discard** drops the edits.

## Console

- An editor for MongoDB shell statements, with a ▶ before each, on a
  database you choose in its bar (`use` switches it too). **Run** runs the
  statement at the caret or those selected; **Run Script** runs them all.
- **Completion** offers the collections and methods after `db.`, each
  method tagged read, write or risky as the safety policy sees it, a
  cursor's methods after a call, the shell's helpers, and the `$`
  operators of queries, updates and pipelines.
- Each statement goes through the safety policy before it is sent, and
  into the [audit log](../audit-log.md), as typed, and the query history,
  its secrets hidden. A `deleteMany` or `updateMany`
  that asks first counts its matches for the question. A script stops at
  a statement that fails or that the policy blocks; at a question you can
  run it, skip it and go on, or cancel the rest.
- Each result gets a pill, beside **Output** (what ran, and errors).
  Documents show in Grid, JSON and Tree views with **Load More** and
  **Fetch All**; other replies show as a JSON tree. Console results are
  read-only: **Open in** opens a find's collection to edit it.
- Statements run on their own, each committed as it runs.

### What the console runs

A subset of the MongoDB shell's language, read by DGopher itself so the
safety policy can judge every statement first:

- `use <database>`, `show dbs`, `show collections`, `show users`,
  `show roles` and `show profile`.
- `db.<collection>.<method>(…)`: `find` and `findOne` with `sort`,
  `limit`, `skip`, `projection`, `hint`, `collation`, `maxTimeMS`,
  `comment`, `explain`, `count` and the like; `aggregate`, `countDocuments`,
  `distinct`; inserts, updates, replaces, deletes, `findOneAndX`,
  `bulkWrite`; index and collection methods.
- Database methods such as `db.getSiblingDB("x")`, `db.runCommand({…})`,
  `db.adminCommand({…})`, `db.createCollection`, `db.stats()` and
  `db.currentOp()`.
- Values in relaxed Extended JSON or the shell's forms: unquoted keys,
  single quotes, trailing commas, comments, `ObjectId()`, `ISODate()`,
  `new Date()`, `NumberInt()`, `NumberLong()`, `NumberDecimal()`,
  `UUID()`, `BinData()`, `Timestamp()`, `MinKey`, `MaxKey` and
  `/regex/flags`.

Refused, with a message that says why:

- JavaScript: variables, functions, loops, assignments and arithmetic.
- Cursor iteration such as `forEach()` or `next()`: results load as you
  scroll.
- `watch()`, `mapReduce()`, and transactions started in the console
  (`session.startTransaction()`).
- Any other method the app does not know: run its command with
  `db.runCommand()`.

## Not yet

Indexes and validation rules to edit, explain plans, export and import,
copies between connections, backup, server activity, users and roles, an
aggregation editor, schema analysis, change streams and GridFS are not
there yet. The AI chat is for SQL editors only.
