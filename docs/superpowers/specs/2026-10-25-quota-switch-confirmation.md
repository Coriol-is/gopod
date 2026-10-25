# Confirmed quota fallback

2026-10-25. Owner-approved scope: offer a switch after quota rejection, require
confirmation, notify the user. CP-010; D031.

## User flow
A known provider usage/session-limit error offers `Переключить и продолжить`
and `Отмена` in the original thread. The question names both providers, says
the entire chat changes, and notes that the target may need `/login`. No model
call, provider mutation or queue insert happens before confirmation.

Confirm is the existing single-use `cf` command-token flow (five-minute TTL).
It calls a hidden `provider.fallback` Router command with turn id and provider
names stored server-side. It has no slash alias, is omitted from help/menus,
and requires `Confirmed`, Telegram source, current registered chat folder,
exact original thread/chat, durable quota offer, and a turn without a final
reply. The command checks its permissions again on press.

The runner compares the expected failed provider inside its chat agent and
lifecycle locks. An old offer never overrides a different current provider.
On success, D030 persists the provider/model reset and preserves sessions.
Then a single durable retry is queued with `source_id=fallback:<original id>`;
its original text, folder, thread, voice flag and attachment path are retained.
A duplicate confirmed action finds the existing retry before changing provider.
The prompt is edited to say which provider was selected and that the request
is queued. Sending that edit can fail without repeating the action.

Cancel, expired tokens and tokens forgotten on restart change nothing. A
confirmed button spent on an error has no live buttons left. If switch fails,
no retry is queued. If switch succeeds but queue insertion fails, the notice
explicitly says the provider changed and asks the user to resend the input.
Switch and queue insertion are separate operations: a process crash between
them may require a manual resend. Do not claim an atomic cross-operation handoff.

## Error boundaries
`UsageLimitError` carries the actual failed-run provider, even when another
conversation later changes the selection. Only existing known allowance
phrases on nonzero CLI exits qualify. Ordinary 429/network/agent/authentication
errors do not suggest switching. Raw CLI output is never included in the UI.

Quota error cooldown uses the turn id, so a second topic or the retry itself
gets its own notice. If the target also hits quota, `fallback:` source identity
suppresses another switch offer and shows the ordinary quota error instead.
A fresh user-initiated Retry remains possible; there is no unattended loop,
automatic switch-back, quota timer or old-model summarization call.

## Limits and verification
The target uses its existing login/API configuration; confirmation does not
perform login. Existing D030 bounded-context and native-tool-state limits apply.
Workspace state must be checked before repeating actions from a failed turn.
Tests use SQLite, simulated CLI processes and a fake Telegram API. Real quota
and live Telegram acceptance remain a deployment-time check.
