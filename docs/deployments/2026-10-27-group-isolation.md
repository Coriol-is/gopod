# CP-020 full group isolation rollout evidence — DRAFT

Date: 2026-10-27. Target: existing RPi4 controller and owner-controlled sandbox.
Source: main `a3a9c53a24456cce40678231ef048e6baba51956`, PRs #66/#67.
Status: implementation complete and merged; rollout blocked on SSH signing.

## Recorded baseline

The last verified production application is `c82399bb22eb3d0bc4ff74fdf48215fbf6f9f3ae`
([P0 release evidence](2026-10-26-isolation-p0.md)). ARM64 release images were
built locally from a clean git archive of merged main `a3a9c53`: controller,
group Claude and group Codex policy v2. They have not been
deployed; the final merged controller binary has not been installed on the Pi.
Groups remain blocked until successful
qualification and explicit scoped activation. This draft records no deployment,
activation, native TCP preflight success, or completed Telegram UAT.

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
  failure of the latest merged source. Final policy-v2 image canaries for both
  providers remain pending.

These observations are narrower than production acceptance. Successful auth-only
checks or synthetic/local protocol fixtures do not prove model inference,
network isolation, delivery or activation for the final deployed runtime.

## Pending release gates and next evidence

1. Restore SSH signing access: the 1Password SSH agent stopped signing; the owner
   has been asked to unlock it; no response is recorded yet. No deployment is
   inferred from the interruption.
2. Finish latest-main policy-v2 image inference qualification for both providers
   and native TCP/network preflight. Record exact final image IDs and bounded outcomes without secrets.
3. Record pre-rollout service/runtime inventory, recoverable backup evidence and
   rollback artifact. Preserve personal sessions, retired group data and active
   legitimate foreground work.
4. Deploy the final merged controller binary/image, verify its revision and
   health, then qualify preparation/activation in the controlled sandbox only.
5. Record resulting namespace/epoch state and real Telegram acceptance separately
   from host/controller checks. Admit no unqualified real group.

No production catalog write, group migration or activation has occurred.

No secret values, auth output, request bodies or private message contents belong
in this evidence file. Append observed results only; do not convert pending items
into passed checks based on source status.
