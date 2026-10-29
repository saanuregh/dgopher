# Charts and dashboards

## Charts

Chart any result from the grid's chart button.

- Types: bar, stacked bar, line, area, stacked area, scatter, pie, and a
  histogram of a column's values in bins with round bounds.
- Axes are detected: a time or text column along x, numbers as series.
- Hover for a tooltip. The palette is safe for color blindness.
- Stacks put positive values above zero and negative ones below. A NULL
  breaks a line or an area, and counts as zero in a stack.
- A chart plots at most 5,000 points.

## Dashboards

A dashboard is a grid of panels, each a read query shown as a chart, rows
or a single value.

![A dashboard of the sample shop: revenue, orders, customers and
products as numbers, revenue by month and customers by country as
charts, and best sellers as a table](../images/dashboard.png)

- **Add panels** from an editor's statement (**Add to Dashboard**), or with
  **Add Panel** in the dashboard's bar. A new panel runs on the connection of
  the first panel, or the project's first connection.
- **Layout:** a grid of four columns; each panel is one to four columns
  wide. **Edit** arranges, resizes and retitles panels, and changes their
  queries, charts and connection (and on PostgreSQL, the database).
- **Any connection:** panels can use any of the project's connections.
  Where they all run in one place, the dashboard's header names it once;
  otherwise each panel shows its connection under its title, and the
  database when it is not the connection's own. A panel whose connection
  fails says why in its place, without an alert.
- **Parameters** are shared by the panels and edited in a bar above them.
  Their values are yours alone, kept apart from the dashboard file.
- **Refresh** by hand (F5), or every 1, 5 or 15 minutes, or every hour,
  while the dashboard shows. Automatic refresh does not reconnect a
  connection that failed; reconnect it from its status bar.
- **Safe by design:** a panel runs one read only. Its statement goes through
  the safety policy and is refused if it writes or would need a
  confirmation. On PostgreSQL and MySQL it runs in a read-only transaction,
  which the server enforces; TiDB has none, so there the safety policy is
  the only check. Each run is audited, as an editor's is.
- **Shared with the team:** a dashboard is a file in the project's
  `dashboards/` folder. It is never overwritten once it changed on disk, as
  by a pull, and reopens with the workspace.
