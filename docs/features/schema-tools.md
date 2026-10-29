# Schema tools

## The table form

**New Table…** on a schema creates a table; **Edit Structure** on a
table's Structure page changes one.

- Columns: rename, retype, make nullable, set defaults and comments, add
  and drop.
- The primary key; indexes, foreign keys (with their actions) and checks,
  added or dropped.
- **Review SQL** shows every statement before it runs. They run in one
  transaction where the engine's DDL allows it, and the confirmation says
  when it does not.
- On DuckDB and in-memory SQLite, whose tabs share one transaction, the
  change waits until an open transaction ends.
- SQLite rebuilds the table for what `ALTER TABLE` cannot change, keeping
  its rows, indexes, triggers and views. It refuses when the table has
  something the form cannot write again, such as a collation.
- DuckDB and ClickHouse change the columns of a table they made, not its
  keys.

## ER diagrams

**View ER Diagram** on a schema draws its tables, with their columns and
keys, joined by foreign-key connectors. Referenced tables sit to the left.

![The ER diagram of the sample shop: customers, orders, order items and
products, joined by their foreign keys](../images/er-diagram.png)

- **Arrange** tables by dragging. A table dropped on another moves to the
  nearest free place, so no table hides another.
- **Open** a table's data by double-clicking it.
- **Change the schema** from the diagram: drag a column onto a table to
  make it refer to that table's primary key. A table's menu drops one of its
  foreign keys, or the table. **New Table** designs one. Each change is
  written as the engine needs it (SQLite's tables rebuilt, their rows kept),
  always confirmed and checked by the safety policy. The diagram then reads
  the schema again, keeping tables where they were.
- **Keyboard:** a table's header takes the keys. ↵ opens its data, the
  arrows move it, and its menu (⇧F10) adds a foreign key from a column.
- **Save** as PNG (drawn at twice the size on screen, up to 16,384 pixels a
  side; a large schema is drawn smaller) or as SVG.
- **Point at a connector** to bring it forward and show its foreign key:
  its columns, and what deleting or changing the referenced row does;
  Alt+click shows it at once.

## Data models

A data model is a schema's tables kept in a file of the project's
`models/` folder, for the team. Open one from the project's menu or the
palette. Its tab shows each table and its DDL.

- **Create:** **Save as Data Model…** on a schema, or **New Data Model…** in
  the project's Data Models section to start from nothing for an engine.
- **Design** without a database, in the same table form: New Table, Edit
  Table and Drop Table save to the model's file.
- **Update** reads the tables again from the schema the model came from.
- **Generate DDL** writes the statements that create the model's tables on
  any SQL engine, in a schema you choose. Each table comes after those its
  keys point at; a key that closes a cycle is added after both tables. On
  another engine, columns take that engine's types with their sizes kept
  (`varchar(40)` stays `VARCHAR(40)`). What the engine cannot have, such as
  another engine's checks, is left out and noted at the top of the script.
  The script opens in an editor, or is copied.
- **Compare** the model with a schema of a connection of its engine (or of
  an engine of its family built on it, as MariaDB on MySQL), or with
  another model. It lists tables missing, differing, or not in the model,
  and for each table the columns, key, indexes, foreign keys and checks that
  differ.
- **Migrate:** the migration makes the schema match the model. Tables are
  created, then changed as the table form changes them (SQLite's rebuilt,
  their rows kept). What the model lacks is dropped only when you ask,
  because a renamed column looks like one dropped and one added. **Apply**
  runs it after a review, through the safety policy; **Open as Script**
  puts it in an editor.

## Query builder

**Build a Query…** on a table writes a `SELECT` from your choices:

- the tables it refers to, or that refer to it, joined by their keys
  (inner or left join), or any table of its schema joined on columns you
  choose;
- the columns, each aggregated or not, with the rest grouped;
- conditions, with the grid filter's operators;
- the order and a limit.

The SQL shows as you build it, and opens in an editor.

## Generate SQL and documentation

- **Generate SQL Script**, on a schema, writes the chosen objects in an order
  that runs: types and functions first, then tables (each after those its
  foreign keys point at), views after what they read, and triggers last. It
  opens in an editor.
- **Generate Documentation** writes one HTML page or a Markdown file listing
  each table's columns, keys, indexes and references, and each object's
  definition.

## Search Objects

**Search Objects…** finds tables, views, columns, routines, triggers,
sequences and types in every schema of a database by name. With **In
definitions**, it also searches views' queries and routines' and
triggers' bodies, showing the line that matched. ↵ opens what it found: a
table's rows, a column's structure, or a definition.

## Rename

Rename a table, a view or a table's column from the navigator. The
statement goes through the same safety review as one typed in an editor.
SQLite cannot rename a view.
