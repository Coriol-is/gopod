# Pi controller rollout — 2026-11-05

## Release and scope

Owner requested Pi only, no worker deployment. Deployed source
`d58cdd2402a2d4026b0be5951ff78cc6382d93de` at 01:57:55 UTC, replacing `a25f22a`.
Exact-main [CI 41951148257](https://github.com/Coriol-is/gopod/actions/runs/41951148257)
passed. PRs #168–#172 had independent review and required CI; linked reviews in
[HANDOFF](../HANDOFF.md) preserve their evidence. Their Sonar PR gates were OK.

Includes queue idle handoff repair, final presentation parity, durable main/fallback
control ownership, positive-receipt DB-ACK repair, and durable photo-to-document
fallback. CP-002 foreground producer remains disconnected; managed tasks remain
off and topic deletion command unavailable. No activation/configuration changes.

## Build and rollout evidence

- Static linux/arm64 controller built from exact release with Go 1.26.10,
  CGO disabled, trimpath and version ldflag.
- Binary SHA256: `756e8641afcc84dd7a7e294cecbbd466a85710e50d45494e90a6ac1626c83260`.
- Image ID: `sha256:bdf20457739aaf06132b19e472513222e30a36632da3f439d60151159a5c4b22`.
- Controller-only overlay on prior exact runtime image. Existing agent images,
  provider binaries/auth homes, workspaces and compose configuration preserved.
- Exact candidate passed isolated readiness with no network, credentials or host
  mounts, read-only filesystem and dropped capabilities before production switch.
- All eight idle counters zero before stop, after stop and after rollout.
- Matched-stopped backup `20261105T015718Z-ms-d53ad460` verified using scratch copy:
  1 file, 44 tables, 9413 rows. This set contains the database; no config files or
  artifact blobs were present in its manifest. It does not back up workspaces,
  provider auth/session homes, IPC or environment secrets; those remain in place.
- SQLite integrity OK. Full-row hashes of registered chats (2), sessions (39)
  and scheduled tasks (15) match before/after. Session mapping unchanged;
  pending adoption zero.
- Controller healthy, zero restarts/OOM; StartedAt `2026-11-05T01:57:55.558855699Z`.
  Owner runtime appeared during post-start processing and is healthy. Six unrelated
  services remain healthy; no group/relay/elevated runtime observed.
- Telegram CLI: `/version` command `3176956`, replying to retained test topic
  `3176309`; correlated response `3176957` at 01:58:14 UTC:
  `gopod d58cdd2 (go1.26.10)`. This proves command routing/version, not native-provider
  or visual-client qualification. No login, model/TTS test or reboot was requested.

## Recovery and remaining work

Prior image retained as `gopod-rollback:d58cdd2-base`, image ID
`sha256:bd20a5427066672184a8958c8c7bbd266981221cd0af533852eeff479dd05439`.
Remote rollout evidence is `/tmp/gopod-release-d58cdd2`; persistent verified backup
is under `/home/spaceinvaderz/gopod-backups`.
No automatic downgrade after candidate startup: old code ignores selected document
branches and durable control ownership. Inspect/settle relevant rows or hold
outbound dispatch before any downgrade; never erase ledgers. Older backup allowlists
may reject newer additive tables. No restore over live data was performed.

CP-002 conditional code alternatives, preview/cancellation/reply-action integration
and foreground cutover remain open. CP-016 unknown-outcome reconciliation,
remaining ownership/native-login/managed settlement and confirmed retirement remain
open. Existing [acceptance gates](../ACCEPTANCE.md) are not closed by deployment.
