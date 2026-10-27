---
name: mygo-upgrade
description: Upgrade DGopher to a newer MyGo release. Reads everything that changed, checks which fork patches and upstream PRs still matter, finds MyGo features DGopher could use, then does the upgrade after approval. Use when asked to upgrade, bump or rebase MyGo or the saanuregh/mygo fork.
argument-hint: "MyGo release to move to (default: latest)"
---

# Upgrade MyGo

DGopher doesn't use MyGo directly. `go.mod` replaces it with a commit on a `dbgopher-v<release>`
branch of our fork, saanuregh/mygo, which is upstream plus our patches. Read the "MyGo" section of
`docs/development.md` first.

Research first, change nothing until the user has seen the report. Every check below depends on
knowing exactly what changed between the two releases.

## Setup

The fork is checked out at `../mygo` (`origin` = saanuregh/mygo, `upstream` = egoist/mygo). Clone
it there if it's missing.

```sh
git -C ../mygo fetch upstream --tags && git -C ../mygo fetch origin
gh release list -R egoist/mygo          # newest release, unless the user named one
git -C ../mygo worktree add /tmp/mygo-NEW vNEW
```

From `go.mod`: OLD is the release in `require`, PIN is the commit hash at the end of `replace`.

Look for patch work that PIN doesn't include: commits on the fork branch after PIN, local
`dbgopher-*` branches that were never pushed, and uncommitted changes in `../mygo`, its worktrees,
or `/tmp/mygo-*` copies. Ask before treating any of it as a patch.

## 1. What changed

Release notes alone miss things, so read all of it:

- `gh release view vX -R egoist/mygo` for every release from OLD to NEW
- `git -C ../mygo log --reverse --oneline vOLD..vNEW` and `git diff --stat` for the same range
- the public API of each MyGo package DGopher imports, at both tags (`go doc -all`, or the diff of
  the generated `ui/api_gen.go`), and the diff of `docs/`
- breaking changes: apply the fork patches to vNEW on a detached commit in `/tmp/mygo-NEW` (as in
  "Do the upgrade", step 1) and build DGopher against it with a copy of `go.mod`
  (`go build -modfile=/tmp/dg.mod ./...`), so the repo isn't touched. Plain vNEW only reports our
  own fork APIs as missing.

Read these closely:

- **Updates.** Diff `updater.go`, `plugins/updater` and `cmd/mygo`. If the update format, its
  signing, the installers or `mygo.json` changed, put it at the top of the report. A mistake here
  means installed copies of DGopher stop updating.
- **Linux libraries.** Compare what MyGo loads at run time in each release:
  `git -C ../mygo grep -h -oE 'lib[A-Za-z0-9_+-]+\.so(\.[0-9]+)*' vX -- ':!*_test.go' | sort -u`.
  For a new one, find what loads it and when. It goes in `flake.nix` (`guiLibraries`) and the
  Linux docs only if DGopher uses the feature that loads it.
- **Go version.** If MyGo's `go.mod` needs a newer Go, bump DGopher's `go.mod` and `flake.nix`.
- **Quitting.** Check that quitting with an open transaction still asks first (DGopher's
  `OnBeforeQuit`), since the hard rules forbid closing one silently.
- **Windows and macOS.** Changes in `*_windows.go`, `*_darwin.go` and the `backend_*` files can't
  be tested here. List them so they get checked in CI or by hand.

For a large range, split the reading across three subagents: updates and packaging (the riskiest,
so the deeper tier), native UI (`ui/...`), and app and platform code (the root package and
`internal/<os>`).

## 2. Our upstream PRs

```sh
gh pr list -R egoist/mygo --author saanuregh --state all --limit 100
```

- **Merged:** which release has it (`git tag --contains <merge commit>`). If that's NEW or older,
  drop its fork patch.
- **Closed:** read why. Did upstream build its own version, or turn it down?
- **Open:** any review comments waiting on us, and does it still apply to NEW
  (`gh pr diff <n> -R egoist/mygo | git -C /tmp/mygo-NEW apply --check`)?

Report only. Don't comment, push or close anything upstream without the user's OK.

## 3. Fork patches

```sh
git -C ../mygo log --reverse --oneline vOLD..PIN
git -C ../mygo cherry -v vNEW PIN        # "-" means upstream already has it
```

Give each patch one verdict, with the evidence:

- **drop**: upstream has it, or DGopher no longer uses it (search for its names with `rg`)
- **replace**: upstream added a different way to do the same thing; list the DGopher code that
  would move
- **keep**: still needed; say whether it applies cleanly to NEW

For each kept patch without an upstream PR, say whether it's worth sending upstream now.

## 4. Doing more with what DGopher already uses

See what DGopher uses:

```sh
rg -o --no-filename '"github.com/egoist/mygo[^"]*"' -g '*.go' | sort | uniq -c
rg -o --no-filename '\b(mygo|ui|editor|markdown|shader|i18n|updater|native)\.[A-Z]\w*' internal main.go | sort | uniq -c | sort -rn
rg -n -i '//.*(mygo|upstream|workaround)' -g '*.go' internal   # comments about MyGo
```

Match that against the changes. Look for fixes that make a workaround unnecessary, new options that
replace DGopher code, changed defaults, deprecations, and new `ui.Tester` helpers for things the
tests now do by hand. Give each idea the DGopher `file:line` it touches.

## 5. New MyGo features

For every new widget, plugin, window feature or `mygo.json` option, either say where it fits in
DGopher and what users would see, or say why it doesn't apply.

## Report

One short section per check, each item with its evidence and a suggested decision, update changes
first. Wait for approval. Pushing the fork, each new feature to adopt, and each "replace" are
separate decisions. The user can approve just the upgrade.

## Do the upgrade

1. In `/tmp/mygo-NEW`, make a new branch `dbgopher-vNEW` (add `-2`, `-3` if the name is taken) and
   cherry-pick the kept patches in order, building `./ui/...` after each and stopping at the first
   conflict. A conflict can come from an earlier patch that didn't apply, and a clean text merge
   can still fail to build, as when both sides add a field of the same name. Never rewrite an old
   `dbgopher-v*` branch: old pins must keep working.
2. Run `go vet ./...` and
   `go test ./ui/... -skip 'TestStrokePolylineGPU|TestOpenGL|TestDiffFrameAllocs'` there. Those GPU
   tests panic without a display and stop the whole package; compare any other failure against
   plain vNEW.
3. Before any code edit, take screenshots on the current version:
   `DGOPHER_SNAPSHOTS=/tmp/snaps-before go test ./...`.
4. Point DGopher at the local copy, which must never be committed:
   `go mod edit -require=github.com/egoist/mygo@vNEW -replace=github.com/egoist/mygo=/tmp/mygo-NEW && go mod tidy`.
   Run the checks in AGENTS.md: vet, gofmt, `go test ./...`, and the integration tests if
   `internal/db` changed.
5. Take screenshots again into `/tmp/snaps-after` and compare them:
   `go run .agents/skills/mygo-upgrade/scripts/pngdiff.go /tmp/snaps-before /tmp/snaps-after /tmp/snaps-diff`
   prints how many pixels changed in each, and where, and writes the changes in red. Many differ
   only by data that changes between runs: times, temporary paths, random rows. Treat every other
   change as a bug until shown otherwise: rerun that test a few times on both versions, and take a
   screenshot one frame later too. Then start the app (`CGO_ENABLED=1 go run .`) and click through
   a connection, a query and a table.
6. Make the approved changes, each with a test, plus any `flake.nix`, Go or packaging updates from
   step 1.
7. Push the fork branch once the user says so, then pin it:
   `go mod edit -replace=github.com/egoist/mygo=github.com/saanuregh/mygo@<commit> && go mod tidy && go mod verify`.
8. Run `go run ./tools/licenses` to update the license notices.
9. Update the patch list in `docs/development.md`, and anything in `docs/`, `README.md` or
   `AGENTS.md` about MyGo that's no longer true.

Run everything once more, remove the scratch worktrees you made (`git worktree remove`), review
the diff, show it and stop. Don't commit, push DGopher or open a
PR unless asked.

## Improve this skill

Last, list what this run taught: commands that failed or were noisy, checks that found nothing
worth having, things you had to find out that the skill should have told you. Suggest edits to this
file, and make them only if the user agrees.
