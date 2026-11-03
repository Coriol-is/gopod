# CP-002 immutable final publication plan

Implementation checkpoint: one durable intent freezes personal reply presentation
before preparation; a ready plan freezes exact rendered parts and immutable
code/voice artifact references. Ready plans publish atomically across all their
preparations and recover at boot without model or TTS calls. Existing production
foreground creation remains off; the new planner is an explicit host entrypoint.

Scope: text/code/voice selection and reply/edit targets; code snapshot failure
keeps code inline; generated audio uses one stable result-bound intent. Pending
preparation, unknown generation and expired/missing preparations stay held. Ready plans
retain active reservations until publication or existing TTL expiry, not forever.

Remaining activation gates: failed document upload currently changes legacy inline
text; immutable plans report partial delivery instead and need an approved
conditional fallback before cutover. Sync voice-only and streaming selection must
remain explicit. Preview ownership, late cancellation settlement, reply actions,
and native producer wiring require integrated qualification. No new flag or live
billing is enabled.

D063 draft: freeze presentation before effects; ready plan bytes and routing are
immutable; one result owns one publication; all code/audio preparation membership
attaches atomically; recovery of ready plans performs no rendering or synthesis.
One new table and additive reply-target column require backup/schema qualification;
old binaries retain no-replay fences but may reject newer schema during restore.

Implementation checkpoint 2: store and host planner implemented; boot consumes
ready intents under existing outbox enablement. No foreground call to
PrepareFrozenFinalReply or WithResultFreeze was added. Unknown/in-progress speech
returns a hold, including voice+text. Unused competing code reservations retain
bounded TTL. Known alternate publication has a separate hold sentinel; corrupted
stored hashes still fail startup rather than silently permit replay.

Validation covers immutable input/plan, exact reply-target persistence, concurrent
publication across reopen, multi-preparation rollback, invalid membership/scope,
legacy populated outbox migration and zero-target hash stability, backup pending
evidence, ready-only boot recovery, text/voice/stream selection, code/edit output,
unknown speech no-rebilling and reply-target transport. Full build/vet/test passed; full affected race suites (store, Telegram, cmd)
passed. Final build/vet and focused races including control idle diagnostics
passed after the migration/transport additions. `git diff --check` passed.
Independent parent source review passed with the requested fixes included.
Docker isolation and CI remain external gates; no native/live acceptance ran.

Actual filesystem loss is detected by the existing dispatcher artifact verifier;
boot validates catalog/preparation membership only. Missing catalog/preparation
or expired reservations hold publication; missing/corrupt blob bytes produce an
existing dispatch failure, never regeneration or replay. No claim of fallback
parity or end-user feature activation is made.
