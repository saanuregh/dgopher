# Known limitations

What DGopher does not do, or does only in part, and why. Planned work is in
the [Roadmap](roadmap.md).

## Connections

| Area | Limitation |
| --- | --- |
| Kerberos | PostgreSQL and SQL Server; MySQL's Kerberos plugin has no Go client. Oracle's is not offered: its driver, go-ora, leaves getting the ticket to the app, and no test server has a Kerberos realm to try it against. On macOS, `KRB5CCNAME` must name a `FILE:` cache, because the system's default cache is not a file. |
| Cancelling behind a proxy | Cancelling a MySQL statement sends `KILL QUERY` for the session's connection ID as MySQL numbers it. ProxySQL or RDS Proxy, which share server connections, may number it differently. |
| Oracle wallets | Only an auto-login wallet (`cwallet.sso`) is read, and of its client certificates only one with an RSA key. The tests use wallets they write as Oracle's tools lay them out, not wallets `orapki` wrote: the test server's image has no `orapki`, which needs Java. |
| Trust prompt | A change to the prompt's wording asks once more for connections already trusted. |

## Engines

| Area | Limitation |
| --- | --- |
| DuckDB files | A read-only and a read-write connection to the same file cannot be open at once: the driver keeps one database per file. |
| DuckDB columns | A column of a table with an index or a key cannot be dropped or retyped, from the table form or a model's migration: DuckDB refuses it. |
| DuckDB transactions | The open transaction is read with `current_transaction_id()`, which DuckDB does not document. A test fails if an upgrade changes it. |
| ClickHouse | Rows cannot be edited in the grid, because ClickHouse changes rows with asynchronous mutations. Use SQL. |
| SQL Server batches | The editor reads every result of a T-SQL batch, each up to the row limit, before it shows the first page: the driver reads the next result only after the one before it. |
| Oracle columns of no length | The Oracle driver, go-ora, reads no rows of a result with a column Oracle describes with no length, as a `NULL` or `''` selected as it is, and fails reading one among other columns. The app reports an error rather than show no rows. Give such a column a type, as `CAST(NULL AS VARCHAR2(1))`. |
| Oracle XMLType | go-ora cannot read an `XMLType` column: a query must select it as text, with `XMLSERIALIZE(CONTENT column AS CLOB)`. Table tabs do so themselves. |
| Oracle messages | `DBMS_OUTPUT` lines show once the statement ends, not while it runs; a query shows those its functions printed while its first rows were read, and the rest with the next statement. At most 1,000 lines are kept; the others are counted. |
| Oracle Explain | Explain fails inside a transaction begun with `SET TRANSACTION READ ONLY`, as `EXPLAIN PLAN` writes `PLAN_TABLE` (ORA-01456). Explain Analyze reads the plan of the newest statement it ran as that user, so two at once as the same user may read each other's; afterwards it sets `STATISTICS_LEVEL` to `TYPICAL`, Oracle's default, not to what the session had. |
| Oracle savepoints | Oracle has no `RELEASE SAVEPOINT`, so grid edits cannot nest in an open transaction: end it first. |
| Oracle Data Pump | Backup and restore run `expdp` and `impdp`, which come with Oracle's Instant Client Tools package (installed with its Basic package) and must be on the `PATH`. The dump file stays in the server's directory: the app cannot copy it here. A restore imports only the objects of the connection's schema, or the one chosen; a dump of another schema imports nothing. Data Pump exits with status 5 when its job ended with errors, as when objects other than tables are already in the schema: the restore then reports a failure, though it replaced the tables; read its log beside the dump. Over TLS, Data Pump needs the connection's wallet. A password holding `"`, a user holding a quote, and a host, service or wallet holding a quote, a parenthesis or `=` are refused, as the parameter file cannot hold them. |

## Data

| Area | Limitation |
| --- | --- |
| Large results | A result keeps at most 200,000 rows (`db.MaxRows`). Export runs the query again and writes page by page, up to the limit it is given. |
| Excel import | A workbook with more than 4,194,304 shared strings, or 128 MB of their text, is refused. [Import files](features/moving-data.md#import-files) lists every import limit. |
