# One-way private access to group data — future design note

Date: 2026-10-25. Status: requested exploration; not implementation or an access
grant. Kept separate from CP-020's P0 containment.

## Direction and authority

A private agent may eventually receive an explicit read grant to selected group
namespaces. A group agent never inherits the reverse grant to private data.
Keep separate origin namespace, target data namespace, actor and intended output
audience. Changing a search target must not change the caller's identity or the
namespace used for writes, extraction, sessions, tasks or IPC.

Example grant: private namespace P may search/list approved history, summaries,
facts or files from group G for a defined purpose. It is read-only, has a policy
revision/expiry and can be revoked. It does not grant arbitrary host mounts,
account connectors, live group membership administration or group publishing.
A group-side policy must authorize the audience/use; owning the bot alone should
not silently reinterpret every group's data-sharing agreement.

Resolve authorization in the controller. Bind capabilities to origin/actor,
explicit target, allowed operations and policy revision; never trust `chat=owner`
or a target folder in LLM JSON. Default deny group→private, group A→group B and
other-private→owner. No owner files/env/auth are mounted into groups as part of a
read grant, and no shared mutable session is created.

## Preventing reverse disclosure

Read permission is not publish permission. Results return only to the private
conversation; any summary or memory derived there remains private by default.
Do not automatically write private-agent summaries into the group's memory or
resume a private transcript inside its runtime.

A private response may combine group data with private context. Treat that output
as private regardless of where some source facts came from. Model instructions
or an automated model-based check cannot prove absence of private information.
Any export back to a group needs a separate approved route: explicit recipient,
reviewed content and scoped one-shot publication authorization, or a fresh
strictly group-scoped workflow that never receives the private context.

Current owner runtime also has direct database/project access, so a scoped
read API alone does not prevent it reading other group state through files. A
truly grant-limited personal reader needs a restricted role/runtime without those
raw privileged mounts, separate from operator administration.

Current owner IPC authority permits cross-chat sends; therefore a read-only
memory grant alone cannot honestly guarantee no reverse disclosure. Strong
one-way mode must also restrict direct public-output tools/network paths and
separate personal preparation from approved publication. Preserve intentionally
authorized existing schedules only through their explicit destination policy,
not by inheriting an unrestricted owner send capability.

## Data and lifecycle boundaries

Prefer scoped query/export operations over mounting entire group folders into
the private container. Return provenance (group namespace, source/time, policy
revision) without secrets or irrelevant participant details. Any cached result
and derived private memory need a defined retention/revocation policy; revoking
future reads cannot make a previous authorized copy disappear automatically.

Group removal, membership/policy changes, namespace retirement and clean epochs
invalidate grants/capabilities. Decide whether private copies must be redacted
or retained under an explicit rule; never silently delete historical data.
Do not promote malicious group instructions into private tool authority merely
because a private agent is allowed to read the text.

## Questions for a later specification

- Which private identities and group policies may grant access, and to which data
  categories/history ranges? Is personal retention of a derived summary allowed?
- Is the first slice controller-mediated read/search only, with public publication
  disabled, or does it include exact-content owner approval for an export?
- How are revocation, provenance and clean namespace epochs enforced across cache,
  summaries, capabilities and active sessions?

Test owner-private P, unrelated private Q and groups A/B with distinct canaries:
P→A granted reads succeed; A→P, B→P and Q→A fail. All writes stay in the origin
namespace, token replay after revocation fails, and public output cannot carry
private canaries through IPC, tools, files, scheduled tasks or connector calls.
Positive public-export controls must require their own authorization and exact
destination/content binding. No group↔private symmetry is inferred.

This note enables no cross-namespace API, data export, account access or
publishing. Start only after CP-020 establishes safe namespaces and boundaries.
