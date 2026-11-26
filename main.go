// DGopher is a native database client for SQL databases and Redis, drawn
// by MyGo without a webview. docs/engines.md lists the engines it supports.
package main

import (
	"fmt"
	"os"

	"dgopher/internal/app"
)

func main() {
	app.Changelog = changelogText
	if err := app.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "dgopher:", err)
		os.Exit(1)
	}
}
