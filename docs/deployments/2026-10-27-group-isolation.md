# CP-020 full group isolation deployment evidence

Date: 2026-10-27. Target: existing RPi4 controller and owner-controlled sandbox.
Source: main `a3a9c53a24456cce40678231ef048e6baba51956`, PRs #66/#67.
Status: controller deployed at 08:58 UTC; group activation remains blocked by live qualification failures.

## Recorded baseline

Production now runs merged code `a3a9c53a24456cce40678231ef048e6baba51956`.
The previous safe P0 controller is retained for application rollback. Groups
remain blocked: production configuration has `EnabledGroups: []`; no production
clean-epoch preparation or activation was performed. Personal images are unchanged.

## Prepared local ARM64 artifacts

These images were built from the merged source without a working-tree `.env`,
SSH forwarding or credential mounts. Native version probes ran with network none.
The controller help and metadata-only catalog preparer ran successfully. No
personal image was rebuilt.

| Image | Immutable image ID |
| --- | --- |
| `gopod-controller:cp020-a3a9c53` | `sha256:2b6e6b7f05774ebfd868cb6002a0edce4d7cabd5480f27811e47945fe1bc40db` |
| `gopod-agent-group:cp020-v2` | `sha256:b206c99c873e553571cef9587e42bb89cc94b3a30622d944e88acec27d2f780a` |
| `gopod-agent-group-codex:cp020-v2` | `sha256:efc8c0d1b15e844b95a9619986e630fc9f312a163873d2c28dc58b9e477c1899` |

Prepared files on the operator Mac:

- `/tmp/gopod-cp020-a3a9c53-arm64-images.tar.gz` — 456131321 bytes;
  SHA256 `b8d3119c2ffb63fdf2510700e61320edc8ec913f32ead2ff738d58a727b16ecf`.
- `/tmp/gopod-cp020-a3a9c53-release-manifest.json` — image/platform provenance.
- `/tmp/gopod-cp020-release-a3a9c53.bundle` — source transfer from the known Pi base.
- `/tmp/gopod-cp020-release-policy/group-codex-catalog.json` — prepared `0600`
  sanitized catalog; same artifact hash recorded below.

The earlier-qualified Pi tool image remains separate: verify its actual ID and
`group-tools`/`group-boundary-v1` labels before reuse. The prepared archive does
not contain personal images or credentials. Temporary files can be regenerated
from the recorded main revision if unavailable after a restart.

## Reviewed payload

Immutable namespace epochs, protected roots and retired-key ownership; separate
personal/group images; read-only existing subscription auth references; trusted
native inference with exact-route HTTPS relay; credential-free no-network tools;
scoped local memory/IPC; owner controls; fail-closed cancellation, drains and
orphan recovery. Personal ASR/TTS credentials are owner-private only. Group speech
and automatic group maintenance remain unavailable. No auth refresh, paid API
fallback, additional personal service integration or T9 reboot is included.

Native protocol interoperability includes validated-and-stripped optional Claude
diagnostics, strict Codex Responses Lite/client-search support, serialized MCP
discovery and fixed diagnostic event classes. See the
[protocol research note](../research/2026-10-27-group-native-protocol.md).

## Evidence obtained before rollout

- PRs #66/#67 and latest-main CI/Sonar are green; independent code/security
  reviews, local full tests, race checks and vet passed.
- Native managed-policy qualification passed 69 cases: all 11 public catalog
  models on new/resume paths, plus hostile capability scenarios. This is local
  protocol/registry evidence, not exact-Pi live-model qualification.
- Policy v2 uses a sanitized public metadata catalog with explicit default
  `gpt-6.1-sol`; artifact SHA256
  `7718d30f843b20ae77c5209208ac77eff668bee5afefc02d9ae65b3934ee0091`.
  See [catalog provenance and preparation](../../container/group/policy/README.md).
- Exact-Pi real offline-tool test passed in 6.22 s.
- Earlier-candidate actual Claude new/resume two-turn qualification passed,
  including file state,
  context continuation and stable session identity.
- Earlier Codex failures preceded the client-search fix; they do not establish
  failure of the latest merged source. Final policy-v2 image results are recorded below; neither full
  new/resume canary passed.

These observations are narrower than production acceptance. Successful auth-only
checks or synthetic/local protocol fixtures do not prove model inference,
network isolation, delivery or activation for the final deployed runtime.

## Exact-Pi qualification and deployment

SSH signing recovered. The transferred archive matched the recorded SHA256.
Docker load on this host normalized image configuration IDs; runtime references
use the actual post-load Pi IDs below, not the local build IDs above:

| Role | Pi image ID |
| --- | --- |
| Controller/relay | `sha256:8eb8d64adee9b12f769ca6a185a9076e01e89218e04a4cba4fedea4f37671c77` |
| Claude group v2 | `sha256:5033d071504c2b4eeb194b0ee77968f598e608cfa46109e29cc5495f360650e1` |
| Codex group v2 | `sha256:169679977d912404935e549e2e75c895add78794629d9b977aeaeac12513e4ce` |
| Previously qualified tools | `sha256:5ab09eeacbb09b5a68209b762b1f3dea8e1338eb49de1c3dfd0cf8aeee8404c1` |

- Both final provider images passed native TCP boundary preflight: direct public
  IPv4/IPv6 and host service ports denied; fixed relay reachable.
- Claude full new/resume qualification failed on the first turn. A separate
  single-turn diagnostic reported unauthorized; the source subscription token
  expiry was confirmed without printing credentials. No refresh/login occurred.
- Codex single-turn no-tool inference returned the expected marker (exit 0).
  Full new/resume qualification failed before first inference. A disposable
  diagnostic build emitting only fixed stage labels localized admission failure
  to the native/workspace/policy tree-budget check after boundary preflight.
  The offending file/type and correction remain unverified. This is not a quota
  diagnosis or successful tool/resume qualification. Diagnostic changes were
  confined to a temporary source archive and were not deployed.
- Initial canary enable failure used local-build IDs absent from Pi; config was
  corrected to inspected post-load Pi IDs before the boundary/model checks.

The existing controller was stopped gracefully with no unfinished user turns.
A stopped-controller SQLite backup is
`/home/spaceinvaderz/gopod-backups/pre-cp020-20261027T085856Z.sqlite`, mode 0600,
integrity `ok`; its protected sibling `.env` backup is retained. This is a DB and
configuration snapshot, not a matched full-filesystem/auth recovery guarantee.
The safe P0 rollback image remains `gopod-gopod:rollback-cp020-p0`,
`sha256:b8b578e8dde2b822f81bb05059f56a9805a65405e6e32cf0bc313010e9e01b45`.

The source checkout was fast-forwarded from P0 to `a3a9c53` through a git bundle.
Production policy and the sanitized public catalog were installed mode 0600;
all groups are disabled. Compose recreated only the controller without building
or retagging personal images. No production group-admin migration was invoked.

## Post-deploy observations

- Controller started `2026-10-27T08:58:57.596978701Z`; inspected running image is
  the Pi controller ID above. Startup reports the full `a3a9c53...` version and
  runner/queue/application ready. Restart count 0, OOMKilled false.
- Startup created the existing personal `gopod-owner` runtime. No group runtime
  was running after the checks. Unrelated host services were preserved.
- SQLite integrity `ok`, 99 completed turns, no unfinished turns.
- All 15 owner sessions, 3 owner schedules and 129 owner memories retained exact
  predeploy selected-column hashes. No schedule pause/resume/edit was issued.
- No reboot, cgroup change, Telegram test publication, new account login,
  credential refresh or paid API fallback was performed.

## Remaining gates

1. Refresh Claude subscription through the existing private owner management
   flow, then rerun its final-image two-turn canary.
2. Codex tree-budget failure: root cause identified and fixed in source on branch
   `fix/codex-group-tree-budget` (see HANDOFF 2026-10-29: arg0 alias symlinks). Review, merge,
   rebuild the Codex group image/controller and rerun the full two-turn canary on the Pi.
3. After both providers qualify, stop the controller, take a fresh backup and
   use revision-checked offline admin preparation/activation for the controlled
   sandbox only. Preserve retired data and intentionally pause legacy group tasks.
4. Record actual Telegram new/resume/tool/delivery acceptance separately. No
   unqualified real group is admitted by this deployment.

Rollback must preserve current DB/epochs and keep shared admission closed; do not
restore a stale DB automatically. No secret values, auth output, request bodies
or private message contents belong in this evidence file.
