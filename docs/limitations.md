# Known limitations

What DGopher does not do, or does only in part, and why. Planned work is in
the [Roadmap](roadmap.md).

## Connections

| Area | Limitation |
| --- | --- |
| Kerberos | PostgreSQL and SQL Server; MySQL's Kerberos plugin has no Go client. On macOS, `KRB5CCNAME` must name a `FILE:` cache, because the system's default cache is not a file. |
| Cancelling behind a proxy | Cancelling a MySQL statement sends `KILL QUERY` for the session's connection ID as MySQL numbers it. ProxySQL or RDS Proxy, which share server connections, may number it differently. |
| Trust prompt | A change to the prompt's wording asks once more for connections already trusted. |

## Engines

| Area | Limitation |
| --- | --- |
| DuckDB files | A read-only and a read-write connection to the same file cannot be open at once: the driver keeps one database per file. |
| DuckDB columns | A column of a table with an index or a key cannot be dropped or retyped, from the table form or a model's migration: DuckDB refuses it. |
| DuckDB transactions | The open transaction is read with `current_transaction_id()`, which DuckDB does not document. A test fails if an upgrade changes it. |
| ClickHouse | Rows cannot be edited in the grid, because ClickHouse changes rows with asynchronous mutations. Use SQL. |
| SQL Server batches | The editor reads every result of a T-SQL batch, each up to the row limit, before it shows the first page: the driver reads the next result only after the one before it. |

## Data

| Area | Limitation |
| --- | --- |
| Large results | A result keeps at most 200,000 rows (`db.MaxRows`). Export runs the query again and writes page by page, up to the limit it is given. |
| Excel import | A workbook with more than 4,194,304 shared strings, or 128 MB of their text, is refused. [Import files](features.md#import-files) lists every import limit. |
