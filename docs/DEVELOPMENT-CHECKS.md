# Development checks

## Pre-commit deadcode

Owner requested this hook on 2026-11-06. Enable it in a checkout:

```sh
git config --local core.hooksPath .githooks
```

The local Git setting is shared by linked worktrees; the relative hook path uses
that worktree's version. Inspect an existing hooksPath/custom hook before replacing
it. Pre-installation inspection found neither in this repository. New clones require installation;
Git does not activate tracked hooks automatically.

Requires Go and Python3. First use downloads/builds pinned
`golang.org/x/tools/cmd/deadcode@v0.49.0` into the user cache; later checks reuse it.
Tool/load/analysis failures fail the commit. Go-source/module/checker changes
trigger the hook; documentation-only commits skip it. Full Go CI remains mandatory.
This hook is not a replacement for tests, race checks, review or CI.

`--staged` materializes the index in a temporary directory. Unstaged callers
cannot hide staged unreachable functions. Analysis targets Linux/ARM64 with CGO
and workspaces disabled, matching Pi, and includes every main plus test executable
(`-test ./...`). It does not execute tests or providers. Other OS/build-tag variants
need separate qualification; dynamically enabled production flags are not runtime
coverage. Assembly/go:linkname limitations also apply.

```sh
python3 scripts/check-deadcode.py           # working tree
python3 scripts/check-deadcode.py --staged  # exact index
python3 scripts/test-check-deadcode.py      # disposable-repository regressions
```

The baseline lists stable package/symbol identities, not line numbers. Initial
16 candidates are unreachable even with tests on both local macOS and Linux/ARM64.
New findings fail. Removed/reachable baseline entries also fail and must be removed
from the baseline. No automatic baseline refresh or blanket exemptions exist.
Review each finding and its intended feature scope before deletion or baseline edits.

## Initial audit — 2026-11-06

At3ea9328, deadcode without test roots reports59 functions; with tests it reports16.
Staticcheck U1000 reports14 production declarations plus one test field. The16
functions span about141 physical source lines; removing them alone cannot close
the Sonar7,111-line gap. These are candidates, not an approved deletion list.
Examples: old `whoamiHandler`/`pingHandler`, reaction wrappers, `Watcher.readTask`,
`Queue.drainPending`, `shellQuoteArgs`, `saveDockerChecked`. Planned disconnected
APIs such as `WithResultFreeze` are not automatically obsolete.

Pinned deadcode JSON and Staticcheck results were saved locally under
`/tmp/gopod-deadcode-*.json` and `/tmp/gopod-unused-staticcheck.txt`; the committed
baseline preserves the exact16 strongest candidates. No source was deleted.
[Go deadcode methodology](https://go.dev/blog/deadcode).

## Sonar scope

Owner excludes test files/fixtures, scripts and all Python code. Both automatic
`.sonarcloud.properties` and CLI `sonar-project.properties` must agree, with project
settings mirrored. These exclusions do not stop Python/Go tests or CI qualification.
Current added exclusions are `scripts/**,**/*.py,**/*.sh,.githooks/**`; no additional Go production
packages are hidden. Earlier main analysis reported57,111 lines including2,351
Python lines: the new exclusion is useful but is not expected to reach50k alone.
Exact effective scope and remaining cap outcome must be verified on a new analysis.

## Validation and merge handoff

Five disposable-repository regressions passed; no application Go file was renamed.
The actual staged hook passed during commit. Independent source/config review PASS
on298d1e3; full `go build ./...`, `go vet ./...`, `go test ./...` PASS (Telegram73.339s).
PR194 initial Sonar analysis15:29:58UTC is OK with zero issue/file components:
changed Python scripts are excluded. Project exclusions were read back after update.
New exact-head CI and review follow final prose reconciliation. The owner explicitly
requested merge/push only, with no deployment. Local hook installation follows merge.
