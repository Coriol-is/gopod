# Elevated profile login qualification — 2026-11-03

Owner requested Telegram login support for the separate elevated profile.
`/root login`, `/login elevated` and the current-generation login button start
only after ROOT provider selection/confirmation. Plain `/login` remains ordinary.

Implementation uses the existing private UI, shared per-chat login reservation,
and the elevated execution lease. Fixed login/auth-status argv are journaled;
stdin codes and provider output are neither journaled nor sent to model context.
Native auth status plus observed exec exit establish success. Cancel/Stop/Close
and restart use the supervisor's own process group. Unknown exit retains the
runtime/capacity block. There is no automatic ordinary credential inheritance.

Verification:

- Full build/vet/test and affected runner/control/telegram/cmd race suites.
- Real Docker supervisor test: stdin reaches interactive child, private login
  output is absent from returned buffers, exit/cancel/reuse behavior preserved.
- Real pinned Claude 2.1.310 and Codex 0.164.0 each produced a parseable native
  login prompt through the supervisor in a disposable unprivileged container.
  Both were cancelled with exit proof; no credentials or host mounts supplied,
  no authorization completed, no raw URL/device code retained in this report.
- Regressions: busy inference refusal, separate auth verification, secret-free
  journal, cancellation/uncertain state, current generation, private router
  alias, >30-line completion drain, reader-error cancellation, cancellation
  before URL publication, and `/stop` while Claude awaits its OAuth code.

Independent review found and corrected completion-drain deadlock, stale prompt
publication after cancellation and `/stop` bypass during sensitive OAuth input.
Final independent review PASS; PR #155 and exact main CI passed; Sonar CLI gate
OK with maintainability findings retained. Production image/controller rollout
and owner-completed browser authorization through the new Telegram path remain
separate acceptance steps; do not claim either from the disposable prompt tests.
