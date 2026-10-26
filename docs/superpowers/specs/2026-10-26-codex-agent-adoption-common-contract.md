# Common contracts for codex-agent feature adoption

Date: 2026-10-26. Status: proposed design only.
[Adoption map](../../proposals/2026-10-26-codex-agent-adoption.md).
All linked adoption specs incorporate these requirements.

## Scope and authority

- The owner authorized saving CPs/specs and later-wave ideas. Proposed defaults
  are reviewable choices, not implementation, account, migration, deletion or
  deployment authority. No feature is marked shipped merely by merging docs.
- Preserve gopod's Go controller, Claude/Codex support, text streaming and current
  personal profile defaults. Reuse existing capability tokens, admission, IPC
  receipts, memory search and runtime qualification instead of a second engine.
- Source inspiration is codex-agent at b231ed572c6b4acaff057e715570b867aa49a75b.
  Its one-owner shared-memory/group model is deliberately not adopted. These are
  behavior/contract adaptations, not copies of private instances, credentials,
  profile text, operational receipts or an automatic SDK/dependency transplant.

## Namespaces, roles and disclosure

- Trusted registration determines personal/group profile. Immutable namespace
  and epoch scope conversation/thread/task/run, sessions, source data, artifacts,
  preferences and delivery. Folder aliases or an approving owner DM cannot change
  execution identity. Display names and model JSON are not authority.
- Personal-only slices can ship independently after their own tests; group use
  fails closed until CP-020 and relevant visibility/role gates are qualified.
  Group grant failure never falls back to personal env, mounts, auth or services.
- Main, worker, research, reviewer, curator and integration roles receive only
  approved host capabilities. End/cancel/revoke invalidates them. Native CLI
  tools, accounts, filesystem and network require actual external enforcement;
  a read-only role prompt or service-tool deny list is not full isolation.
- Retrieved pages, attachments, forwards, memory and peer messages are data.
  They cannot mint owner instructions, confirmations, preferences or grants.
- Read authority and publication authority are separate. One-way personal reads
  of group data remain a separate explicit grant; groups get no reverse access.
  Private/service replies and failures never turn public as fallback (CP-019).

## Durable truth and bounded work

- Distinguish admitted/queued execution, model completion, goal claim, host
  verification, external side effect, delivery and user acceptance. A model
  claim cannot create host_verified, delivered or owner_accepted evidence.
- Use transactional IDs/revisions for admission and publication, but do not claim
  exactly-once remote effects. Unknown execution/send states remain unknown and
  cannot authorize automatic replay. Cancellation cannot undo remote actions.
- Every run has one explicit final-delivery owner: legacy agent IPC or a newly
  opted-in controller-result path. Scheduled stdout is not new delivery authority.
  Do not send a second final response when an existing task already used IPC;
  preserve the current CP-015 contract until an explicit migration is approved.
- Limits cover wall time, concurrency, input/output bytes, artifact storage and
  service calls. Token observations can inform next-turn admission; they are not
  a hard in-flight spend ceiling or proof of subscription quota availability.
- Extend existing observability with bounded content-free correlation/status
  receipts where needed. Keep private outcomes/provenance in scoped storage;
  never export prompt, secrets, precise coordinates or arbitrary tool output as
  generic telemetry. No universal native/browser/plugin interception is claimed.
- Preserve the selected subscription/provider and existing confirmation policy;
  no silent paid API migration or provider fallback. Pin and requalify actual
  supported CLI/model/platform combinations when native behavior changes.

## Migration, rollout and evaluation

- Specs state logical contracts; executable plans must map them onto current
  schemas without blind status renaming or destructive data migration. Preserve
  legacy records with unknown authority instead of fabricating provenance.
- Use matched backups and isolated restore rehearsals before relevant production
  schema/filesystem rollout (CP-007). Artifact references require artifact bytes.
  Never start a second production poller while testing a restored instance.
- New integrations stay disabled by default. No user account, map billing, peer
  credential, heavy image or automatic learning is enabled by a saved design.
- Acceptance matrix: private and group A/group B; both providers; new/resume;
  denied/expired/revoked grants; crash before/after commit/send; cancellation and
  unknown effects; oversized/malicious input; migration/rollback; source routing.
- Reuse native mock-provider/runtime contracts already under experiments/ and
  add feature-specific deterministic fixtures. A successful fake, container
  health or installed tool is not native-account, Telegram or quality acceptance.
  Live calls, rendered report inspection, Pi resource qualification and user
  acceptance are separate evidence with the necessary authorized scope.
