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

Read `go.mod` from `origin/main`, not the local checkout, which may be behind:
`git fetch origin && git show origin/main:go.mod`. OLD is the release in `require`, PIN is the
commit hash at the end of `replace`. When PIN is on a `dbgopher-main-<date>` branch, the range
starts at its base on upstream (`git -C ../mygo merge-base PIN upstream/main`), not at vOLD. Make
DGopher's changes in a worktree from `origin/main`.

Look for patch work that PIN doesn't include: commits on the fork branch after PIN, local
`dbgopher-*` branches that were never pushed, and uncommitted changes in `../mygo`, its worktrees,
or `/tmp/mygo-*` copies. Ask before treating any of it as a patch.

## 1. What changed

Release notes alone miss things, so read all of it:

- `gh release view vX -R egoist/mygo` for every release from OLD to NEW
- `git -C ../mygo log --reverse --oneline vOLD..vNEW` and `git diff --stat` for the same range
- the public API of each MyGo package DGopher imports, at both tags (`go doc -all`, or the diff of
  the generated `ui/api_gen.go`), and the diff of `docs/`
- breaking changes: build DGopher against vNEW with a copy of `go.mod`
  (`go build -gcflags=-e -modfile=/tmp/dg.mod ./...`, then `go vet` the same way for the tests),
  so the repo isn't touched. If the fork patches apply cleanly, apply them on a detached commit in
  `/tmp/mygo-NEW` first. If they conflict, don't rebase yet: in a second throwaway worktree of
  vNEW, add our fork's APIs as no-op stand-ins (fields on the structs, methods returning zero) and
  build against that. What still fails is upstream's change. `go test ./...` on it shows changed
  behaviour too; failures in the stubbed features are expected.

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
  `OnBeforeQuit`, `quitAllowed` in `internal/app/run.go`), since the hard rules forbid closing
  one silently. Look for new quit paths that can't be cancelled, as `QuitEvent.SessionEnding`
  (0.9.0), and for updater relaunches.
- **Theme defaults.** A new `ui.Theme` field left zero takes MyGo's default, which may not suit
  DGopher's own themes in `internal/ui/widgets`: `InactiveAccent` (0.8.0) turned chosen rows
  mid-gray in windows not in front. Draw such states with `Tester` helpers, as
  `SetWindowActive(false)`, and look.
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
   can still fail to build, as when both sides add a field or method of the same name: the
   drawn-menus patch's `engine.drawMenu` met upstream's `PopoverMenu` one in v0.7.4, and was
   renamed `handMenuToApp`; after 0.7.4 its `engine.drawn` field meets upstream's repaint one,
   and upstream PR #422 renames it `appMenu`. Resolve each conflict inside its own patch's commit. Never rewrite an
   old `dbgopher-v*` branch: old pins must keep working.
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
   only by data that changes between runs: times, temporary paths, random rows. To set those
   aside, take a second set on each version (the old one with `-modfile` and a copy of the
   committed `go.mod` and `go.sum`), diff each version against itself, and drop the screenshots
   that change on the old version anyway. Treat every other change as a bug until shown otherwise:
   rerun that test alone about five times on each version (`go test -count=1 ./internal/app
   -run '^TestName$'`) and compare the PNGs' hashes, and take a screenshot one frame later too.
   The full run's load causes differences a lone run doesn't, and some tests flake on the old
   version too. A screenshot whose data changes each run (a temporary path) is dropped as noise,
   so check what the change touches there by hand. Then start the app and click through a connection, a query,
   a table and a context menu. To drive it from the shell, build it with a throwaway file
   `//go:build e2e` importing `github.com/egoist/mygo/plugins/automation` (delete it afterwards),
   run it under `Xvfb` with `DGOPHER_CONFIG_DIR` set to a scratch folder and
   `MYGO_AUTOMATION=$XDG_RUNTIME_DIR/dg-e2e.sock` (a Unix socket path must stay under about 108
   characters), and use `mygo-automation` from `plugins/automation/cmd`. The dev shell has no
   `Xvfb`: `nix build --no-link --print-out-paths nixpkgs#xvfb` gives it, then
   `<path>/bin/Xvfb :97 -screen 0 1400x900x24 &` and `DISPLAY=:97`. `mygo-automation` takes its
   session from `export MYGO_AUTOMATION_SESSION=<name>`; a `--session` flag goes after the
   command, never before it. The tool's guide says context menus can't be driven, but that is
   about the system's: DGopher draws its menus in the window, so `click <ref> --button right`
   opens one and `pointer move <ref>` opens a submenu. "Try the sample" creates
   a project in `~/dgopher-projects` by default: fill in a scratch folder instead.
6. Make the approved changes, each with a test, plus any `flake.nix`, Go or packaging updates from
   step 1.
7. Push the fork branch once the user says so, then pin it:
   `go mod edit -replace=github.com/egoist/mygo=github.com/saanuregh/mygo@<commit> && go mod tidy && go mod verify`.
   CI fetches through the default module proxy, so check that it serves the new commit too:
   `GOMODCACHE=$(mktemp -d) GOFLAGS=-modcacherw go mod download github.com/saanuregh/mygo@<version>`,
   with the version `go.mod` now names; remove that cache afterwards.
8. Run `go run ./tools/licenses` to update the license notices, only after step 7: with a local
   path in `go.mod` it names MyGo as `egoist/mygo` instead of the fork's version. Nix's Go has
   no `LICENSE` in its GOROOT, so the tool stops there. Point `GOROOT` at a scratch folder of links to
   `$(go env GOROOT)/*` plus `LICENSE` and `PATENTS` from a cached
   `~/go/pkg/mod/golang.org/toolchain@*` of the same Go version, run it, and remove the folder.
9. Update the patch list in `docs/development.md`, and anything in `docs/`, `README.md` or
   `AGENTS.md` about MyGo that's no longer true.

Run everything once more, remove the scratch worktrees you made (`git worktree remove`), review
the diff, show it and stop. Don't commit, push DGopher or open a
PR unless asked.

In zsh, `echo ==` fails (`=` expands a command name): quote it.

## Improve this skill

Last, list what this run taught: commands that failed or were noisy, checks that found nothing
worth having, things you had to find out that the skill should have told you. Suggest edits to this
file, and make them only if the user agrees.
