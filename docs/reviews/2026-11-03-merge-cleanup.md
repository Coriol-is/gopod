# Merge and worktree cleanup — 2026-11-03

Owner explicitly authorized merging remaining work and deleting branches and
worktrees. Initial inventory: 82 worktrees, 86 local branches and 54 remote
branches, plus the symbolic origin/HEAD reference. No open PRs remained.

## Integration and preservation

One unintegrated documentation branch was found: CP-035 CLI hooks research.
Commit `7eb73fe` was cherry-picked as `ed1320a`, preserving the current proposal
registry while adding the missing survey and proposal. It remains proposed;
no experiments, hooks, provider calls or runtime features were activated. Links
and whitespace checks passed. No application source changed or deployment ran.

Other branches were verified against merged PR heads/merge ancestry, equivalent
cherry-picked patches, aggregate patch IDs or identical integrated source/files.
Notable non-ancestral evidence:

- P0 worker patches match reviewed integration `47e12a8`, merged as `5c36b81`.
- Parallel-wave topic patches together match `b62eb22`; remaining patches are
  equivalent to main, with only newer operational documentation differing.
- Stream-integration source matches `81f5f08`; its report is retained. Later
  acceptance documents supersede the old checkpoint.
- The task IPC replay report and fixture are byte-identical in main.
- The old release qualification report is retained and extended with fixes.

The sole dirty worktree contained an earlier elevated-rollout HANDOFF correction.
Its complete file and binary patch were saved and byte-verified before restoring
that obsolete worktree to HEAD. The ignored P1 scaffold .gitignore was preserved
as well. Metadata, SHA256 manifest, per-ref proof, removal logs and preserved files
are in `<git-common-dir>/cleanup-archives/2026-11-03/`; keep that archive.
No backup bundle was created. Main's local settings, credentials, workspace data,
backups and unrelated repositories were left in place.

## Final layout

- 80 obsolete worktrees removed using normal `git worktree remove`, without force.
- 53 remote branches deleted in one atomic push after exact-head revalidation.
- 85 obsolete local branches removed, including the final session branch.
- Canonical checkout: main, synchronized with origin/main.
- Current session checkout: detached at the same final documentation commit.
- Only main remains as a local/remote branch; origin/HEAD remains a symbolic alias.

Cleanup did not change production: last accepted application release remains
`a25f22a`. Existing CP-002/CP-016 activation gates remain in HANDOFF. Full source
checks and exact release CI were already green; this task adds documentation only,
so focused link/diff/consistency checks apply.
