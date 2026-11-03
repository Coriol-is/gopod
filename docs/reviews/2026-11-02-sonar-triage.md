# SonarCloud triage on `main`, 2026-11-02

Project `Coriol-is_gopod`, branch `main`, triaged against `main` `d8866c9`.

## TL;DR

- **The `main` numbers describe a six-day-old tree.** Every SonarCloud
  analysis of `main` since `77fd0bb` (2026-10-27, CP-020 merge) ends as
  "SonarQube Cloud analysis failed" (check run conclusion `cancelled`), checked
  on each of the 40 most recent `main` commits and on every
  commit sampled back to `77fd0bb`. The last successful `main` analysis is
  `4e3031a` (2026-10-27 00:01 +0100). The 11 vulnerabilities, 166 code smells and
  the E security rating all come from that snapshot. Packages added since then
  (`internal/backup`, `internal/artifactfs`, `internal/groupsandbox`, `cmd/...`)
  return 404 from the measures API, which confirms the snapshot is stale.
- **No vulnerability is a confirmed true positive.** 8 of the 11 are Go SQL
  statements built by concatenating compile-time constant fragments, with every
  value bound as a parameter. Rule and line come from file-level counts (method below).
  Verdict: false positive. 2 are Docker
  `npm install -g` lines (rule `docker:S6505`, inferred); verdict: accepted,
  because the files cannot change in this PR. 1 (in `internal/telegram`) could
  not be located.
- **No code changed in this PR.** No true positive was found, and the HIGH/BLOCKER
  issue list could not be read (see "Access limits").
- **The gate should not be treated as a merge signal until `main` analysis
  works again.** Proposal at the end.

## Access limits: what could and could not be verified

| Source | Result |
|---|---|
| SonarQube MCP `search_sonar_issues_in_projects` | Returns 0 issues for `Coriol-is_gopod` with any filter (projectKeys, branch, files, severities, PR 137). Without a project filter it returns issues of `Midlibrary-io-Dev_ml4` only: the tool is bound to the first organization of the token (`midlibrary-io-dev`), not `coriol-is`. It cannot list gopod issues. |
| SonarQube MCP `get_component_measures` | Works for the project, directories and files, so vulnerabilities could be located at **file** level. It gives neither rule nor line. |
| SonarQube MCP `search_security_hotspots` | Works: 1 hotspot (below). |
| Public SonarCloud API | `api/components/show` answers "Project doesn't exist" without authentication (private project). No token was used. |
| GitHub check runs | SonarCloud posts a summary only, with no annotations and no review comments. The `main` check summary says the analysis failed; the failure reason is only visible in the SonarCloud UI. |
| Browser (`claude-in-chrome`) | Extension not connected; the SonarCloud UI was not read. |
| Local tools | `go vet` clean. `staticcheck` installed (results below). `gosec` v2.21.4/v2.22.4 via `go run` do not build or run on Go 1.27.3 (x/tools and type-loader errors), so no gosec run. |

Every rule and line attribution below is therefore an **inference**: the file
counts come from SonarCloud, and the rule and line come from matching the file
at `4e3031a` against the count. It is marked as such.

## Vulnerabilities (11 total, 7 in the new-code period)

File-level counts from SonarCloud (snapshot `4e3031a`): `internal/memory` 5
(`memory.go` 3, `compiler.go` 2), `internal/store` 3 (`turns.go` 3),
`internal/telegram` 1, `container` 2 (`Dockerfile` 1, `Dockerfile.codex` 1).
Every other directory queried reports 0: `runner`, `control`, `ipc`, `config`,
`scheduler`, `isolationproxy`.

### V1-V3: `internal/store/turns.go` (3)

- Location at `4e3031a`: lines 151, 171, 209. On `main`: lines 168, 188, 226.
- Inferred rule: SQL query built by string concatenation (Go SQL-injection rule
  family). These are the only three concatenated queries in the file, which
  matches the count of 3.
- Code: `` `SELECT `+turnColumns+` FROM turns WHERE id = ?` `` and two siblings.
  `turnColumns` is a package `const` (line 58). Every runtime value (`id`,
  statuses, `source`, `sourceID`) is passed as a `?` bind argument.
- **Verdict: false positive.** No runtime data reaches the SQL text.
- Proposed action: mark "Safe / False positive" in SonarCloud. Optionally
  inline the column list in the three statements if the noise matters. Not done
  here.

### V4-V6: `internal/memory/memory.go` (3)

- Location at `4e3031a`: lines 218, 391, 439, each `` ...'active'`+vis+` ... ``.
  These are the file's only three concatenated queries, which matches the count.
  On `main` the queries moved to `internal/store/memory_queries.go` (lines 39,
  139, 185: `ListMemoriesInThread`, `SearchMemoryVectors`,
  `SearchMemoryText`). `memory.go` no longer contains SQL.
- `vis` comes from `store.MemoryVisibility(alias, thread)`. That function
  concatenates only literal SQL and `alias`, and every call site passes a literal
  (`""` or `"m"`). `thread` is returned as a bind argument, never formatted into the text.
  The FTS query text from the memory API (`q`) goes through
  `SanitizeMemoryFTS` and is bound with `MATCH ?`.
- **Verdict: false positive.**
- Proposed action: mark FP once `main` is re-analysed (the issues will reappear
  under `memory_queries.go`).

### V7-V8: `internal/memory/compiler.go` (2)

- Location: three concatenated queries exist (`fetchPinned` line 105-112,
  `fetchRecentByKind` line 124-143 with `q += vis`, `fetchByKindSemantic`
  line 160-169). Sonar counts 2, so which two is not known.
- Same pattern: constant fragments plus `visibleIn` (literal alias); `kind`,
  `chatFolder`, `cutoff`, `limit` and the query vector are bind arguments.
- **Verdict: false positive.**
- Noted, not a vulnerability: `compiler.go` (and `obsidian.go:342,378`,
  `local_group.go:82,91`) call `m.store.DB()` directly, which goes against the
  CLAUDE.md rule "DB access goes through `internal/store`". Moving these
  queries into `internal/store` (as was already done for `memory.go`) would also
  remove the Sonar noise. It is a refactor beyond this finding and is left for a
  separate change.

### V9: `container/Dockerfile` (1) and V10: `container/Dockerfile.codex` (1)

- Location: `container/Dockerfile:61`
  `RUN npm install -g @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}` and
  `container/Dockerfile.codex:29` `RUN npm install -g @openai/codex@${CODEX_VERSION}`.
  These are the only package-manager installs in either file.
- Inferred rule: `docker:S6505` "JavaScript package manager scripts should
  not be executed during installation" (type VULNERABILITY, CWE-506/829).
- Evidence: the package versions are pinned by build arg, the install runs at
  image-build time on the owner's machine, and these are the two vendor CLIs
  the container exists to run. A compromised release would already run as the
  agent at runtime, so `--ignore-scripts` buys little against that threat.
- **Verdict: accepted** (real pattern, low marginal risk). This PR must also not
  modify these files: a test reads them byte-for-byte.
- Proposed fix (separate PR): try `npm install -g --ignore-scripts ...`, check that
  both CLIs still start (`claude --version`, `codex --version`) since either may
  rely on a postinstall step for its native binary, and update the
  byte-for-byte test fixture in the same commit. If a CLI needs its postinstall,
  mark the issue Accepted in SonarCloud with that reason.

### V11: `internal/telegram` (1): not located

- File-level probes returned 0 for `media.go`, `voice.go`,
  `capabilities_http.go`, `login.go`, `login_handoff.go`, `handler.go`,
  `bot.go`, `streaming.go`, `status.go`, `lifecycle.go`, `inbound_context.go`,
  `actions.go`, `cmdbuttons.go`. A local review of the package at `4e3031a` (SQL,
  file I/O, outbound HTTP, crypto, hard-coded secrets, templates, test fixtures)
  found no candidate sink.
- **Verdict: unverified.** Read it in the SonarCloud UI (Issues → Type
  Vulnerability → Directory `internal/telegram`) and add it to this document.

### Security hotspot (1)

`Dockerfile:23` (host image), `docker:S6471`: "The alpine image runs with root
as the default user". Status TO_REVIEW. The host process talks to the Docker
socket, and running it as non-root needs a socket group mapping. Design work,
not a quick fix. Recommendation: review it and mark Acknowledged with that reason,
or track a non-root host image as a separate change.

## HIGH / BLOCKER issues

**Not available.** The MCP cannot list gopod issues and the UI was not
reachable, so no HIGH/BLOCKER issue on `main` (or the two code smells that set
PR #137's maintainability rating to B) could be read. Nothing was fixed under
this heading. The vulnerabilities above are reported at MAJOR by their rules
(`docker:S6505` is MAJOR / Security MEDIUM). The Go SQL rule severity is unknown.

## Code smells (166 total, 153 in the new-code period)

Grouping by rule needs the issue list, which was unavailable. Distribution by
directory (snapshot `4e3031a`):

| Directory | Code smells |
|---|---|
| `internal/telegram` | 46 |
| `internal/runner` | 45 |
| `internal/control` | 13 |
| `container` | 8 |
| `internal/ipc` | 7 |
| `internal/store` | 1 |
| rest of `internal/` | 24 |
| outside `internal/` and `container/` | 22 |

Recommendation, to apply once the per-rule facet is read in the UI:

- **Fix as touched:** duplicated string literals (`go:S1192`) and empty
  function bodies without a comment (`go:S1186`). These are cheap and local.
- **Accept, do not mass-refactor:** cognitive complexity (`go:S3776`) and
  long parameter lists (`go:S107`) in `telegram` and `runner`. Refactoring
  them for the metric goes against "don't improve code the task didn't ask you to
  touch". Fix them only when the function is changed for a real reason.
- **Consider disabling or tuning** any rule whose findings are idiomatic Go or
  test-only (for example naming rules firing on `_test.go` table names). Decide
  per rule from the facet, not wholesale.
- Docker smells in `container/` (8): accept for the byte-for-byte-tested
  Dockerfiles. Fix them only together with the fixture update.

Local proxy, `staticcheck ./...` on `d8866c9` (not Sonar, for orientation): 61
findings. 26 ST1005 (error string style), 10 SA1019 (deprecated Docker
SDK and `httputil.ReverseProxy.Director`), 7 ST1013, 2 SA4009, 1 SA4006, and
U1000 unused code (`restartAnnounce`, `saveDockerChecked`,
`similarCandidates`, `sanitizeFTS`, `drainPending`). The two SA4009
(`internal/runner/runner.go:189,236`, "argument tier is overwritten before
first use") are intentional fail-closed code: the tier is forced to
`TierOwner` after `authorizeOwner`. Not a bug.

## How the findings accumulated

1. The SonarCloud gate runs on PRs, where it judges only the PR diff. The PR
   gates were mostly green, so the debt never blocked a merge (PR #137's B
   rating came from 2 smells on 12 new lines).
2. The `main` branch gate was already red on 2026-10-24..26 ("Quality Gate
   failed" on every `main` commit). Nothing reads that status, and branch
   protection checks PR status only.
3. From `77fd0bb` (2026-10-27) every `main` analysis fails outright. The `main`
   dashboard froze at `4e3031a`, so its "new code" covers 46k of 47k lines. The
   new-code period seems to be "previous version" or a date before the
   project's first analysis, so almost everything counts as new.
4. Likely cause of the failures (**hypothesis, verify in SonarCloud → Administration /
   Billing**): `ncloc` at the last good analysis was 47,378. A rough local count
   puts `77fd0bb` about 9k lines higher and current `main` at roughly twice the
   size. That crosses the 50k-LOC allowance of a free-plan private project
   right at the commit where failures start. PR analyses keep working,
   which fits a limit that applies to the main-branch total.

## Gate proposal

1. **Restore `main` analysis first.** Confirm the failure reason in the UI. If it
   is the LOC limit, either exclude non-product paths from analysis
   (`experiments/`, `docs/`, test fixtures; set `sonar.exclusions` in the
   SonarCloud project settings, since automatic analysis is used and there is no
   `sonar-project.properties` in CI) or move to a plan that covers the size.
   This needs the owner's decision.
2. **Keep the PR gate** as it is (it works and catches new issues on the diff).
3. **Set the new-code period to a fixed reference**: "specific analysis" or a
   version tag cut after the first successful re-analysis. The `main` gate then
   measures drift from a known baseline instead of the whole codebase.
4. **Make the `main` gate visible**: add a scheduled (weekly) CI job, or
   a step in the deploy checklist, that fails when the `main` quality gate is
   ERROR or the latest `main` analysis failed. Query
   `api/qualitygates/project_status?projectKey=Coriol-is_gopod&branch=main` with
   a `SONAR_TOKEN` repository secret. Adding the secret needs the owner. Do not
   add it to branch protection on PRs, because it would block unrelated merges.
5. After re-analysis, triage the remaining issues in the UI: mark the 8 SQL
   findings False positive with a link to this file, decide on the two Docker
   findings, locate V11 and read the rule facet for the smells.
6. Separately: make the MCP query the `coriol-is` organization (configure the
   SonarQube MCP server with that organization), so agents can list gopod issues.
