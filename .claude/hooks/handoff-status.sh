#!/usr/bin/env bash
# Reports how stale docs/HANDOFF.md is relative to the commits that
# landed after it was last touched. Wired to SessionStart in
# .claude/settings.json so every session opens knowing the real state,
# and runnable by hand (`.claude/hooks/handoff-status.sh --text`) when
# you just want to look.
#
# Exits 0 always — a broken status check must never block a session.
set -uo pipefail

HANDOFF="docs/HANDOFF.md"

emit_json() {
  # $1 = context text
  if command -v jq >/dev/null 2>&1; then
    jq -n --arg ctx "$1" \
      '{hookSpecificOutput: {hookEventName: "SessionStart", additionalContext: $ctx}}'
  else
    printf '%s\n' "$1"
  fi
}

fail_quiet() {
  [ "${1:-}" = "--text" ] && printf '%s\n' "$2" || emit_json "$2"
  exit 0
}

MODE="${1:-}"

# Anchor on the script's own location, not the caller's cwd — a hook may be
# invoked from anywhere, and the answer must always describe this repo.
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P) || exit 0
root=$(git -C "$here" rev-parse --show-toplevel 2>/dev/null) || \
  fail_quiet "$MODE" "handoff: not a git repo, skipping status check."
cd "$root" || exit 0

[ -f "$HANDOFF" ] && [ -n "$(git log -1 --format=%H 2>/dev/null)" ] || \
  fail_quiet "$MODE" "handoff: no $HANDOFF or no commits yet."

last_sha=$(git log -1 --format=%h -- "$HANDOFF" 2>/dev/null)
if [ -z "$last_sha" ]; then
  fail_quiet "$MODE" "handoff: $HANDOFF has never been committed — write the first entry."
fi

behind=$(git rev-list --count "${last_sha}..HEAD" 2>/dev/null || echo 0)
last_date=$(git log -1 --format=%ad --date=short -- "$HANDOFF" 2>/dev/null)
stamp=$(grep -m1 '^\*\*Last updated:\*\*' "$HANDOFF" 2>/dev/null | sed 's/^\*\*Last updated:\*\* *//')
dirty=$(git status --porcelain 2>/dev/null | grep -c . || true)

# The stamp inside the file lying about the real commit date is its own
# failure mode — surface it separately from the commit-count drift.
stamp_note=""
if [ -n "$stamp" ] && [ "$stamp" != "$last_date" ]; then
  stamp_note=" The \"Last updated: $stamp\" line disagrees with the file's actual last commit ($last_date) — fix the stamp too."
fi

if [ "$behind" -eq 0 ]; then
  msg="handoff: docs/HANDOFF.md is current (last touched $last_date in $last_sha, no commits since)."
else
  msg="handoff: docs/HANDOFF.md is $behind commit(s) behind HEAD — last touched $last_date in $last_sha. Read it, then update \"Current state\" / \"What's done\" / \"What's next\" before you stop working.${stamp_note}"
  if [ "$behind" -ge 5 ]; then
    msg="$msg This gap is large enough that the file likely misdescribes the project; verify against ROADMAP.md rather than trusting it."
  fi
fi

[ "${dirty:-0}" -gt 0 ] && msg="$msg Working tree has $dirty uncommitted path(s)."

if [ "$MODE" = "--text" ]; then
  printf '%s\n' "$msg"
else
  emit_json "$msg"
fi
exit 0
