# CP-013 — Preserve speech transcript provenance

| | |
|---|---|
| Status | done |
| Date | 2026-10-25 |
| Origin | Owner requested metadata so the agent knows input came from a voice message |
| Size | S |
| Touches | Telegram transcription-to-prompt normalization |
| Depends on | D030 |
| Spec | Bounded change: fixed source kind, automatic transcription marker, positive duration in seconds; no schema migration |

## Behaviour
After successful transcription, label the agent input as `voice_message`,
`audio_file` or `video_note`, with `transcription=automatic` and a positive
`duration_seconds` when present. Keep an audio caption separate from ASR text.
No filename/id, speaker identity, confidence, language or emotion is inferred.
Apply the label after raw slash/login routing. Existing reply mode is unchanged.
Store the label in queue text so retries, recovery and provider handoff retain it.
Failure/empty transcription must not produce a fabricated successful transcript.

## Acceptance
Fake-Whisper handler tests verify voice/audio/video-note labels, exact transcript
text, duration handling, caption separation, failure handling, raw slash/login,
thread isolation and SQLite provider-handoff preservation. Live acceptance is
separate; old stored transcripts are not backfilled with guessed metadata.
