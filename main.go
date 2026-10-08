// DGopher is a native database client for PostgreSQL, MySQL,
// ClickHouse, SQLite, DuckDB and Redis, drawn by MyGo without a webview.
package main

import (
	"fmt"
	"os"

	"dgopher/internal/app"
)

func main() {
	if err := app.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "dgopher:", err)
		os.Exit(1)
	}
}
