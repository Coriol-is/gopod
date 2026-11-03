# Attachment refusal notice correction

Live acceptance found a missing-source PDF refusal notice in main while the
successful retried PDF arrived in its originating topic. The notice also claimed
that the missing file remained in the workspace.

A detailed refusal now retains the host capability's validated conversation
through preparation, publication and bounded retry rejection. The Telegram sink
revalidates the scope before each part and sends to that exact topic, without
legacy main fallback. Invalid/missing capability, malformed request and disabled
artifact routes produce only a generic folder-authorized notice, without request
paths or content. Legacy sinks cannot reroute a topic-specific notice to main.

Receipt help and user wording no longer assert file existence or location after
a failed attachment. Existing accepted artifact and ordinary IPC permissions do
not change. This introduces no request-controlled topic routing.

Targeted routing/refusal tests, full build/vet/tests and IPC/Telegram race suites
passed. Regressions cover trusted topic and main notices, malformed/missing/stale
and foreign-target requests without path disclosure, absent scoped sinks,
quota retry exhaustion retaining the topic, stale scope rejection and deletion
between notice parts. Independent source/test review passed. Diff/link checks
passed. No production deployment has occurred in this worktree.
