# PR 55 Sonar review

Reviewed 2026-10-26 against `1e47a231973547838d12f8854ec11b0bb346426c`.
Source: current GitHub Sonar check `121302753791` and its four annotations.
The annotations expose titles/locations, not Sonar's complete authenticated
taint trace. No account/model requests or production changes were performed.

## Findings

| Annotation | Location | Review verdict |
| --- | --- | --- |
| Command Argument Injection via faulty LLM-supplied CLI arguments | `live_probe.py:144` | No reachable model-input path found; recommend False Positive for this source revision, subject to checking any additional Sonar trace |
| Cognitive complexity 40, allowed 15 | `live_probe.py:282` | Valid maintainability finding; split `sanitized_events` into event counting, item counting, Claude inventory and report assembly |
| Exception test contains multiple possible throwing calls | `test_live_probe.py:186` | Valid test clarity finding; instantiate `Cancellation` before the `assertRaises` block |
| Same exception-test finding | `test_live_probe.py:200` | Same fix; the assertion should contain only the `capture` call |

Only the security annotation is named in the failed quality-gate condition
(`C Security Rating on New Code`, required A). Application CI and the isolation
qualification job both pass at the reviewed revision. The maintainability
findings are worth fixing but are not the stated security-gate failure.

## Security source-to-sink review

The flagged sink is `subprocess.Popen` in `capture`:

- The executable is the literal `docker`, with `shell=False`. Allowed operation
  names are `create`, `start`, and `exec` (`docker_command`, lines 59–67).
- Call sites are trusted canary/runtime builders, version/auth checks and lifecycle
  code. They are not registered as model tools or exposed as an HTTP handler.
- Docker option boundaries use `--`. The canary prompt is generated from fixed
  markers and provider/turn enums, then passed as bounded stdin (`execute_turn`,
  lines 456–470), not appended as executable options or shell source on the host.
- The returned Codex thread identifier is parsed into a UUID before reuse
  (`codex_session`, lines 422–430). Other returned model text does not become
  Docker management arguments.
- Actual untrusted MCP input follows a different graph:
  HTTP/stdio request -> `dispatch` -> `call_tool` -> strict `mcp_command` ->
  `Executor.run`. It does not call `live_probe.capture`.
- That Executor fixes its image, mount, namespace and Docker flags outside model
  input. Shell source is passed to `/bin/sh -c` inside the restricted worker;
  shell execution there is the intended capability, not host shell execution.

No exploitable LLM-to-Docker-management argument injection was found in the
current entry points. This does not establish production-wide isolation or
approve arbitrary reuse of the helper.

## Important limitation

`docker_command` does **not** validate the complete Docker argument grammar or
bind every call to an owned runtime. A trusted Python caller could supply
`docker exec` arguments for another container. Consequently the helper is not
an authorization boundary and must not be exposed through MCP, Telegram or
other untrusted requests. Its current safety depends on the internal call graph.
If it becomes a production component, use namespace-bound typed operations,
not a generic list-of-arguments interface.

## Recommended disposition

1. Fix the three maintainability annotations in focused edits, without changing
   security semantics or repeating authenticated inference tests unnecessarily.
2. Review the complete Sonar trace if available. If it identifies no additional
   source edge beyond those reviewed here, mark this individual security issue
   False Positive with the source-to-sink rationale above.
3. Keep the check enabled. Do not exclude the experiment, use blanket suppression
   or override the red gate to merge. Another round of cosmetic subprocess
   rewriting does not substitute for reviewing the alleged untrusted source.

## Verification

Focused offline suite: 16 tests discovered, 15 passed, 1 Docker opt-in skipped;
0.472s. It covers prompt-via-stdin, hostile option-like input remaining data,
binary/operation rejection, UUID/session handling, auth gates, cancellation,
bounded output/input and safe proof handling. This review did not repeat live
subscription calls. The existing CI qualification job is green.

Security Review Progress:
- [x] Step 1: Map entry points and trust boundaries.
- [x] Step 2: Load OWASP Security guidance and relevant review checklist.
- [x] Step 3: Sweep current Sonar annotations and source call sites.
- [x] Step 4: Triage source reachability, authorization and impact.
- [x] Step 5: Record findings, limits and recommended disposition.
