# Personal/group isolation audit and remediation decision

Date: 2026-10-25. Source baseline `ea38581e95ae214ff2a4c0dd5fc8f7f40b1f7d86`.
Status: audit and implementation plan; broad isolation changes **not applied**.
The private-login transport hotfix was separately merged/deployed as01c18fe;
its actual client/auth acceptance remains pending. That transport correction is
not evidence that all container boundaries are safe.

**Verdict: current registered-group containers do not meet the requested
personal-data/connected-service isolation contract.** SQL chat scoping is real,
but filesystem, environment and credential authority bypass that boundary.

User contract: a group cannot retrieve personal chat facts; personal services
connected through Claude/OpenAI are disabled there. Integration exceptions need
an explicit group grant, separate nonpersonal identity/project/mailbox, narrow
capabilities and independently scoped credentials. Gmail/Google Cloud are not
intrinsically safe brands. Default deny applies until a grant is approved.

## Work performed and evidence limits

- [x] Map entry points and trust boundaries: group text/files, registration,
  history/memory, per-exec capability, IPC, mounts, environment, CLI homes,
  account integrations and runtime reuse.
- [x] Load applicable OWASP/Go guidance and current official CLI documentation.
- [x] Sweep source and pinned CLI behavior for candidate issues.
- [x] Trace each reported issue to reachable data/authority; distinguish fixtures
  from production observations and administrator configuration hazards.
- [x] Report findings, preserved protections, staged fixes and acceptance limits.

Two agents audited memory/filesystem and env/services independently; the root
agent additionally examined IPC/auth boundaries and a metadata-only runtime
inventory. Pinned offline clients: Claude Code2.1.310 and Codex0.164.0.
No production secret values were examined or included in agent output/evidence;
Docker inspection was filtered remotely to env names and mount metadata. No
personal document contents or account connector data were read. No authenticated service/model request, live exploit, credential
revocation, deletion, group permission change or broad isolation mutation was
performed by this audit.

Production Docker metadata for an existing nonowner registered group confirms:
`/workspace/vault` mounted RO, and env key names `OPENAI_API_KEY`,
`FREEFEED_BASE_URL`, `FREEFEED_USERNAME`, `FREEFEED_PASSWORD` present. Values were
not displayed, logged, retained in evidence or used for authentication. Root filesystem is RO, capabilities dropped, network
is ordinary Docker bridge. Those Docker hardening controls do not restrict the
sensitive data explicitly granted through mounts/env.

Detailed evidence:

- [Memory/filesystem audit](2026-10-25-isolation-memory-filesystem-audit.md)
- [Environment/services audit and verified CLI controls](2026-10-25-isolation-env-services-audit.md)
- [Six-PR remediation plan](../superpowers/plans/2026-10-25-personal-group-container-isolation.md)

## Findings

| ID / severity | Concrete path and impact | Evidence / limit | Required correction |
|---|---|---|---|
| I01 High — personal vault read (CWE-200, A01) | `standardMounts` gives every registered tier the configured vault; group shell reads personal notes directly, outside memory API scope | Source and synthetic real builder fixture; live mount target confirmed. No personal content read | Owner-private vault only; group receives only a separately published, sanitized dataset through an explicit grant |
| I02 High — personal/global credentials (CWE-522, A01) | Main's global env allowlist is appended for every tier and copied from host env; group gets FreeFeed credentials and a cross-provider OpenAI key | Source chain and synthetic env test; live env **names only** confirmed | Deny-by-default profile env; selected approved inference identity separate from independently scoped integration secrets; no fallback to personal env |
| I03 High — host bind-source symlink escape (CWE-59, A01) | `/workspace/chat` exposes RW parent of separately bound `memory`; group renames it, makes a symlink to a known host private root, then a recreated container resolves that host source | Real `EnsureChatDirs` + `BuildMounts` fixture accepts synthetic DataDir/owner-memory target. No real Docker exploit or private access | Put controller-controlled bind roots outside agent-writable ancestors, root/nofollow host access, prevent source replacement/TOCTOU before Docker resolution |
| I04 High — account/service authority bypass (CWE-862, A06) | Subscription account connectors can be fetched despite fresh local home; readable account auth + Bash/network permits alternate CLI/direct HTTP bypass of top-level disabled tools | Official account/CLI behavior and pinned offline flag verification. Actual private account integrations were not enumerated | Dedicated integration-free inference identity or credential broker plus immutable policy and enforceable route/network boundaries; flags alone are insufficient |
| I05 High — policy revocation not effective on reuse (CWE-863, A01) | Running container reuse compares image identity, not effective env/mount/service policy; changing config can leave old personal authority alive | Source; no live policy mutation | Trusted effective-policy fingerprint, deny stale admission and serialize recreation under existing chat slot; do not stop active foreground work implicitly |
| I06 Medium — IPC directory metadata escape (CWE-200, A01) | Watcher unrooted `os.ReadDir` follows group-writable channel symlink to outside directory; rooted payload read blocks contents, but source-scoped error receipt reveals outside basename | New synthetic Go-overlay regression PASS0.268s. No production directory enumerated; contents not leaked by the fixture | Enumerate opened namespace-root directory handles with nofollow, not host `ReadDir`; retain rooted read/archive/receipt controls |
| I07 Medium — mutable alias/profile identity hazards (CWE-863, A06) | Memory/session/IPC authority uses editable folder names; reserved `owner` and stale namespace reuse rely on trusted registration choices, and owner tier is zero value | Configuration/owner-only mutation hazard, not demonstrated unauthenticated registration bypass | Immutable namespace binding/retirement, explicit fail-closed profiles, reserved aliases and no private-to-group reuse |
| I08 High — group login secret delivery (CWE-200, A01) | Existing `/login` sends URL/device code/raw CLI result to originating group, and OAuth input is persisted before interception | Confirmed baseline source; values not printed. Transport hotfix01c18fe deployed separately; live auth/client acceptance and historical cleanup remain unqualified | Private owner confirmation tied to target group; no public fallback, no raw output/logging or active auth input retention; group runtime tier must stay restricted |

A01/A06 refer to OWASP Top10:2025 Broken Access Control/Insecure Design. Ratings
reflect the requested untrusted-group boundary. A missing vault/env setting
removes that specific data source, not the general defect. Connected-service
risk is conditional on the reused account's authority; this report does not
claim that Gmail or Google Cloud were accessed.

## Boundaries that already work

Memory compiler slots, thread visibility, search/list/write and ordinary control
memory commands are scoped by exact chat folder. Per-exec capability tokens bind
folder/thread in the controller; caller-supplied `chat=owner` cannot override
that authority. Native session/neutral history keys include chat and provider.
Group folders do not normally receive the raw SQLite/project mounts reserved
for the owner. Separate CLI homes exist per chat folder.

IPC authorization rejects nonowner cross-chat sends/tasks. Rooted request,
archive and receipt operations prevent the tested content/symlink-write escapes;
I06 is a narrower enumeration/filename leak and must not be exaggerated into
arbitrary private file content access. Docker drops capabilities and uses an RO
root. Owner-only registration remains a real permission check; I07 is a design
footgun within trusted configuration, not a new remote registration exploit.

These protections do not compensate for I01–I04: a valid group process can read
an explicitly mounted note/credential without attacking SQL or the capability.
No prompt instruction can make an already-readable personal secret inaccessible.

## Immediate plan and decision boundaries

Start with independent P0 containment, not a prerequisite database rewrite:
fail closed for unqualified group execution, remove personal env/vault/skills,
close rooted IPC enumeration, and explicitly drain/recreate affected runtimes
only in a reviewed rollout. This audit does **not** pause or recreate them.
P0 can ship without choosing new API billing or completing namespace migration.

The linked six-PR plan then covers immutable namespace/protected roots,
inference identity and explicit integration grants, immutable CLI policy and
host-enforced egress, policy fingerprint/reconciliation, non-destructive clean
epoch migration and adversarial acceptance. Do not call a flag-only restriction
strict isolation while the agent still holds personal OAuth and network access.

An API-only restricted key/project can change subscription economics; do not
silently convert billing. A broker must prove native CLI compatibility and
route/method enforcement, not only DNS allowlisting. Dedicated subscription
identity must be supported and have no personal connected-service authority.
Any approved integration is scoped to a group and a dedicated identity; personal
Gmail/Cloud/Drive credentials are never an implicit exception.

Old group state may contain information already retrieved before hardening.
Unmounting a vault does not remove it from transcripts, files or memories.
Preserve/quarantine legacy state and use a new restricted epoch where needed;
no automatic deletion, credential rotation, service revocation or history
carryover is authorized by the audit. Rollback cannot restore old unsafe mounts
just to regain group availability.

## Additional follow-ups and uncertainty

Interactive login currently closes its attachment/EOF on cleanup; verified
process-group termination is not established. Add managed auth-exec lifecycle,
pre-URL read cancellation and explicit cancellation semantics as a separately
qualified follow-up. Private transport and a generic success status do not prove
upstream tokens were revoked or a CLI process died.

A group may need only model inference, or explicitly approved integrations. That
policy must be recorded per namespace instead of inferred from a folder name,
service brand or human owner sending a message inside a group. Displaying a
login screen in a private DM must never turn its target into the owner's
memory/container/credential namespace. The separate hotfix is required hygiene,
not completion of the full isolation plan.

Live account behavior, effective network enforcement, dedicated credential
scopes, safe content carryover and actual restricted-group acceptance remain
open. Tests must use synthetic private canaries and allowed positive controls;
no production credential or personal file is an appropriate audit test payload.
