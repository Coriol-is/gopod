# Telegram input fidelity and media understanding

**Date:** 2026-10-25  
**Status:** Three delivery queues accepted for planning; implementation deferred  
**Proposal:** CP-014; transcript metadata baseline CP-013

## Goals
Let users send material naturally and ask questions about its actual content.
Preserve links, source kind and evidence anchors. Never confuse a filename,
caption or OCR guess with processed source content. The owner requested the
three queues below; implement them as separate bounded phases with live gates.

## Verified current state
| Input | Current implementation |
|---|---|
| Voice | Whisper transcript → agent; voice reply mode retained |
| Audio file | Whisper transcript, with optional typed caption; text reply |
| Video note | Audio transcription only; no frame analysis |
| Ordinary Telegram video | No video handler; caption-only text or ignored without caption |
| Photo | Download to chat uploads; caption includes path; single captionless photo has a known missing-path prompt gap |
| Document / video sent as file | Download and pass path; no universal content extraction pipeline |
| Album | Collect photos/documents; audio/video album members skipped |
| Card text/caption | `messageText` drops entities, keyboard URLs and via-bot metadata |
| Guest queries addressed to gopod | SDK has fields, but active update subscriptions/default handler do not implement them |

CP-013 labels successful speech input with fixed media type, automatic-ASR
provenance and known duration. That label is not visual/video understanding.
Current native workspaces are shared **per chat**; thread isolation concerns
conversation history/retrieval, not a separate filesystem sandbox per thread.

## Queue 1 — Receive faithfully and state what is available

### Cards, text and links
Normalize text/caption together with their entities; preserve visible label and
actual URL separately. Handle Telegram offsets in UTF-16 code units, with
astral emoji, nested/overlapping spans and malformed bounds in tests. Preserve
code/pre/quote structure, links and meaningful formatting without interpreting
third-party text as privileged instructions. Prefer a bounded structured input
record over rebuilding arbitrary Markdown/HTML and reparsing it.

Extract provided URL-button labels/targets from reply markup. Describe action
buttons without replaying their callback payload. Do not click buttons, invoke
a different bot, complete login actions or fetch URLs merely during normalization.
Unknown/unsafe schemes remain descriptive data, not implicit network operations.
No URL may be invented from a card title or substituted with an OCR guess.

Preserve available photo/document paths alongside the caption, including the
captionless-photo gap. Retain source-kind and available public bot attribution
without exposing hidden forward sender details. Keep user text/slash/login
routing unchanged; normalization for the model is applied after command routing.

### Guest-mode boundary
The supplied screenshot is evidence of a visible card, not of a particular
Bot API update shape. First capture a sanitized representative update showing
which fields Telegram delivered; do not claim the exact lost URL is recoverable
from the existing plain-text store or screenshot alone.

Distinguish:
1. A card created by another bot that arrives as a normal `Update.message`.
   Normalize only its supplied text/entities/media/keyboard/source fields.
2. An actual `Update.guest_message` addressed to **gopod**. It uses
   `guest_query_id`/`answerGuestQuery` and has a distinct reply/authorization
   context. Do not enable it by aliasing it to a normal message/chat id.

The API warns that a guest query's chat identity may not coincide with ordinary
bot chats using the same identifier. Guest participation therefore needs its
own principal, namespace, owner policy and one-response route, without inherited
access to an owner's ordinary chat memory/tasks/credentials. It is a separately
reviewed subphase, not required to preserve links in normal delivered cards.
Limit bot-to-bot interactions and reject unattended response loops.

### All attachment types
Classify each input. Unsupported or partially processed input gets a clear
notice instead of silence or an answer pretending the file was examined.
Represent availability explicitly: caption only, file downloaded, text extracted,
audio transcribed, selected frames analysed, extraction failed, or limit exceeded.
Phase gate: a synthetic card fixture with a hidden URL and a URL button reaches
the agent with the **supplied** targets; no guessed address. A real owner-provided
card fixture must subsequently pass. Unsupported ordinary video is acknowledged
truthfully before deeper video processing ships.

## Queue 2 — Useful document, image and speech analysis

| Scenario | Result | Evidence anchor |
|---|---|---|
| Ask a question about a PDF | Relevant answer and excerpts | Actual PDF page |
| Scan or screenshot | OCR plus interpretation, with uncertain text marked | Image/page/region when supported |
| DOCX/presentation | Summary, decisions, actions or version comparison | Paragraph/section or slide; no invented page numbers |
| CSV/spreadsheet | Explain data, compare rows, extract a table | Sheet/cell/row with preserved values |
| Multiple documents | Compare versions or reconcile facts | File identity plus source location |
| Image | Describe visible content, compare supplied images, read a diagram | Image/region; no unsupported identity claims |
| Voice/audio/video note | Transcript, summary or action list | Source kind; timestamps only if actually produced |

Keep the original attachment and extraction provenance distinct from the model's
interpretation. Never infer speaker identity or vocal emotion from an ASR text.
Source references must point into actual extracted content, not fabricated
pages/timestamps. A failed extraction still leaves a useful, truthful response
and preserves a retry path; it never masquerades as an empty successful file.

Use an explicit attachment descriptor: kind, display name, detected MIME/type,
known size/duration, scoped file handle, derived artifact references and extraction
status. Validate type and resource bounds before processing. Archives and
executable/document active content are not implicitly executed or expanded.
A parser runs with bounded time/output, minimal access and explicit cancellation.
Its availability and supported formats must be verified in each agent/runtime
image; installing a utility is not proof of extraction quality.

## Queue 3 — Video timelines, questions and derived materials

### First slice: speech and sparse frames
Accept both normal Telegram video and a video attachment sent as a document,
including forwarded inputs. Treat every album member explicitly. Retain original
file provenance and transcript source type; inspect duration/format first.

Create a timeline from the audio transcript and a bounded set of sampled or
scene-selected frames, when the selected provider supports image analysis.
Do not stream every frame or load an unlimited video into RAM. A silent video
can use frames; an audio-only job can use speech. If a capability is unavailable,
state exactly what was processed. Key questions:
- What happened, and what are the main segments?
- What is shown around 02:15?
- Which actions, decisions or instructions appear in this recording?
- Where in the clip does a supplied topic or visible event occur?

Answers reference available timestamps and identify whether their evidence is
speech, a frame, or both. Audio and visual tracks remain separate; disagreement
is not silently resolved into an invented event. Sampled frames cannot establish
what happened between them, and ASR cannot establish what was shown visually.

### Follow-on slices, after the first gate
- SRT/VTT transcript export and a concise chapter/timeline digest.
- Extract screenshots or user-requested short clips with source time ranges.
- Turn a demonstration/meeting into a checklist, tutorial or action list.
- Compare two supplied recordings or find a cited segment across retained media.
- Optional media library/search with retention controls, implemented separately
  from transient analysis. No automatic collection of unrelated chat history.

Generated files are new artifacts linked to their original file/time range.
User-requested transformations are explicit operations; sending a card or file
alone does not authorize publishing it, contacting another bot or external upload.

## Cross-cutting execution contract
- Chat authorization comes from the registered chat path; retrieval defaults to
  the current thread and main is not a wildcard. Chat-shared filesystem storage
  is stated honestly; stronger per-thread file secrecy requires a separate design.
- Store input/job identity before expensive work. Durable stages cover received,
  downloading, extracting, ready and failed; restart resumes without duplicating
  model replies or losing already acknowledged input. D020/D030 continuity applies.
- Add a bounded media-worker admission gate, progress, Stop and cleanup. Existing
  agent admission does not bound a separate decoder/OCR worker automatically.
- Qualify configurable limits on bytes, duration, pages, frames, wall time, derived
  output and concurrent jobs. On the Pi, memory cgroup enforcement is currently
  unavailable; do not advertise a hard RAM cap or enable unqualified heavy
  decoding. Preserve host responsiveness and reserve space for other services.
- No remote extraction service, new provider, recurring processing or billing
  path is enabled by this spec. Offloading and operational changes need explicit
  approval. An intentional analysis request stays within configured budgets.
- Keep original/derived content, download credentials and query tokens out of
  ordinary logs. Retention/deletion semantics cover source and derived artifacts.
- Persist normalized content/provenance so retries, quotas and provider switching
  retain it, with explicit truncation when context budgets omit detail.

## Acceptance and rollout
Each queue needs representative fixtures, malformed-input/resource/cancellation
checks, real-provider capability tests, and owner-visible Telegram acceptance.
Do not call media supported solely because it downloaded or a fake test passed.
Queue 1 can ship without queues 2/3; advanced video does not block fixing links.
Document every supported/partial/unsupported path in a user-facing capability
matrix and update it when an extractor is actually qualified.

## Sources verified 2026-10-25
- [Telegram Message](https://core.telegram.org/bots/api#message): text/caption,
  entities, via-bot, media and reply markup; guest query identity caveat.
- [MessageEntity](https://core.telegram.org/bots/api#messageentity): hidden link
  URL and UTF-16 offsets.
- [Guest bots](https://core.telegram.org/bots/features#guest-bots) and
  [Update](https://core.telegram.org/bots/api#update): guest invocation and updates.
- Repository: `internal/telegram/handler.go`, `inbound_context.go`, `media.go`,
  `voice.go`, `bot.go`; SDK `go-telegram/bot` v1.27.0 models. Existing raw updates
  are not archived in full, so this case's actual received link fields are unverified.
