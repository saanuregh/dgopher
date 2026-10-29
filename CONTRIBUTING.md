# Contributing to DGopher

Thanks for helping. This page covers how to propose a change. How to build,
test and find your way around the code is in
[Development](docs/development.md).

## Before you start

- **Bugs and ideas:** open an issue first for anything larger than a small
  fix, so the approach can be agreed before you write it.
- **Security problems:** do not open an issue. Follow
  [SECURITY.md](SECURITY.md).
- **Read the safety rules.** Read [Safety model](docs/safety.md) before
  changing anything that runs statements.

## Rules every change keeps

These protect users' databases. A change that breaks one will not be
merged.

- **Every statement goes through `internal/safety`** before it reaches the
  server. A statement it cannot classify counts as a write.
- **Grid edits apply in one transaction,** and each statement must change
  exactly one row by primary key, or everything rolls back.
- **Secrets never reach project files, logs or the audit log.** Pass text
  that is shown or logged through `internal/redact`.
- **An open transaction is never closed silently.** Closing a tab,
  disconnecting or quitting asks first.
- **Engine behavior is not only in `Dialect`.** Switches on `db.Engine` are
  spread across packages; search for `case db.<Engine>` when changing an
  engine.
- **DuckDB and in-memory SQLite share one connection** (`DB.single`), so
  all their sessions share one transaction.

## Make the change

1. Fork the repository and create a branch named `<type>/<short-slug>`, for
   example `fix/data-view-layout` or `feat/saved-query-parameters`.
2. Make the change, with tests. [Testing](docs/testing.md) explains how to
   write unit, UI and integration tests.
3. Update the docs when behavior changes: the area's page under
   [Features](docs/features.md) for what users see, and [Safety model](docs/safety.md) for anything that
   runs statements.
4. Run the checks, which must all pass:

   ```sh
   go vet ./... && gofmt -l .                   # both print nothing on a clean tree
   go test ./...
   DGOPHER_IT=1 go test ./...                   # when the change touches internal/db
   ```

   CI runs gofmt, vet and `go test ./...` on Linux and Windows, but not the
   integration tests. Run those yourself;
   [Development](docs/development.md#integration-servers) starts the
   servers.

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/): a type,
an optional scope, and a short plain-English summary.

```text
feat(redis): stream consumer groups
fix(query): keep the editor's height after a restart
docs(readme): link the safety model
```

- **Types:** `feat`, `fix`, `docs`, `refactor`, `perf`, `test`, `style`,
  `build`, `ci` and `chore`.
- **Scopes:** the area changed, such as `app`, `ui`, `editor`, `query`,
  `db`, `safety`, `redis`, `keys`, `er` or `audit`.

## Pull requests

- Keep one change per pull request.
- Say what changed and why, and how you tested it, including which engines.
- For a UI change, add a screenshot. `DGOPHER_SNAPSHOTS=/tmp/snaps go test
  ./...` writes PNGs of the UI under test.

## License

DGopher is licensed under the [Apache License, Version 2.0](LICENSE). By
contributing, you agree that your contribution is licensed under it too.
