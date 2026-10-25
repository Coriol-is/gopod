# Agent startup readiness

The controller restores saved per-chat provider/model choices before recovering
turns, starting scheduled work or accepting Telegram commands. It then warms
only the configured owner's already-registered workspace. Other chats remain
lazy; unregistered owner configuration does not register a chat or spawn agents.

Restore failures never silently select the default provider. A failed global
registration read aborts initialization. A failed/unknown per-chat selection
blocks that chat's agent execution; explicit successful `/provider` selection
repairs it. Other restored chats and control commands remain available.

## Local check

A startup check holds the chat's agent slot, with a 15-second work deadline, five-second cleanup reserve and
five-second individual CLI checks. It ensures the selected provider image is
running, checks its CLI version and local authentication status, and registers
the warmed container with idle cleanup. A model choice is restored metadata;
no model weights or conversational process are started by this check.

Running containers are compared with the selected immutable image ID. Ordinary
Ensure cannot replace a mismatched running container; startup reconciliation
under the chat slot can. Failed image inspection preserves the existing
container. No blanket removal occurs just because a saved provider is restored.

Probe cancellation performs bounded process-group cleanup before slot release.
Docker outages or escaped/detached processes retain the existing best-effort
cleanup limits; an observation timeout alone is not proof a process died.
Checks never adopt/clear a native session, modify history or change tasks.
Only safe states and numeric CLI versions enter startup logs.

## Management-layer external check

After successful local readiness, the controller performs one bounded startup
management observation. For Codex, the documented App Server stdio exchange is
initialize, initialized, account/read with refreshToken:false and, for ChatGPT
subscription auth, account/rateLimits/read. No thread, turn, prompt, login or
logout method is sent. The controller keeps credentials inside the selected
agent container. Normal CLI account/cache behavior may still occur.

API-key authentication has no ChatGPT subscription quota: report not_applicable.
For Claude subscriptions, a supported cold-start headless quota endpoint has
not been established; report unknown/unsupported instead of scraping OAuth
endpoints or sending a dummy prompt. Local auth status remains available.

The initial Codex read reports the default quota bucket only; it does not
aggregate product-specific buckets or claim cross-product readiness.
Quota windows and explicit allowance/spend-control fields retain missing/null
states. Do not infer recovery from a reset timestamp or usage percentage.
Neither an authenticated account nor an observed quota establishes that a
particular model inference will succeed. Inference remains unverified.

The probe has a ten-second overall budget including cleanup and does not repeat
per thread/message. Periodic polling, manual refresh/cache UI, model catalog
checks and passive Claude status-line ingestion are separate future slices.
The result is recorded in safe startup logs; it does not change provider choice,
automatically resume/retry a turn or override quota-switch confirmation.

See [official-interface research](research/2026-10-25-provider-management-readiness.md).
Live evidence must distinguish container/CLI/auth observations, quota snapshots
and real model responses. No model request is required for startup acceptance.
