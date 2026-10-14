# Security policy

DGopher connects to production databases and handles their passwords, so
security reports are welcome and taken seriously.

## Report a vulnerability

Report it privately through GitHub:

1. Open the repository's
   [Security tab](https://github.com/saanuregh/dgopher/security).
2. Choose **Report a vulnerability**.
3. Describe the problem, how to reproduce it, and what an attacker could do
   with it.

Please do not open a public issue or pull request for a vulnerability
until a fix is released.

## Supported versions

Fixes go into the latest release. Update to the
[latest release](https://github.com/saanuregh/dgopher/releases/latest)
before reporting, and say which version you tested.

## In scope

Problems in DGopher itself, for example:

- A statement that bypasses the [safety policy](docs/safety.md): a write or
  destructive statement that runs without the confirmation its environment
  requires, or a write on a read-only connection.
- A secret that reaches a project file, a log, query history or the audit
  log, or a password sent to a destination other than the one it was saved
  for (see [Secrets](docs/safety.md#secrets)).
- A project file (`dgopher.json`, a query file, a dashboard, a data model,
  or a shipped `.dgopher/` folder) that makes the app read or write files
  outside the project, run a command, or connect without asking.
- An audit log that can be changed without
  [Verify Integrity](docs/audit-log.md#tamper-evidence) noticing.
- TLS or SSH host verification that accepts what it should refuse.

## Out of scope

- What a database server does once it accepts a statement. For a hard
  guarantee, connect as a role with only the privileges it needs (see
  [What the policy cannot catch](docs/safety.md#what-the-policy-cannot-catch)).
- Items already listed in [Known limitations](docs/limitations.md).
- Unsigned installers: the builds are not signed yet.
