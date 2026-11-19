package main

import _ "embed"

// changelogText is CHANGELOG.md, whose notes the app shows after an update.
//
//go:embed CHANGELOG.md
var changelogText string
