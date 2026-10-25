# Media delivery plan — three queues

Status: planning accepted 2026-10-25; no new extractor or guest-mode route is
implemented by this plan. CP-014; spec is the implementation contract.

## Queue 1: Input fidelity (first)
1. Capture a sanitized real card/update fixture; preserve the screenshot's
   failure scenario without inventing its original URL or copying private data.
2. Add typed text/caption/entity/keyboard normalization, UTF-16-safe offsets,
   known bot attribution and explicit link targets. Keep raw command/login paths.
3. Always include known attachment paths; fix the captionless-photo gap.
4. Classify unsupported/partial input and notify users, especially normal videos
   and skipped album members. No silent loss or claims of examining absent content.
5. If gopod guest participation is requested, design and test the distinct guest
   authorization/reply namespace separately; do not just subscribe and alias updates.
6. Unit/integration fixtures, privacy/loop checks, owner card and topic acceptance.

## Queue 2: Documents, images and speech (second)
1. Define attachment/provenance/job records and format capability matrix.
2. Implement bounded extraction one format slice at a time: text/PDF, OCR,
   document/slide and tabular input, image tasks and timestamped speech as qualified.
3. Persist extraction stages before work; support progress, Stop, retry, restart,
   scoped retrieval, cleanup and context-budget truncation.
4. Verify answers against actual page/slide/paragraph/cell/image references,
   malformed documents, unavailable parsers and attachment namespace isolation.
5. Qualify the selected extractor/provider combination on the actual deployment
   host; expose partial/unsupported formats honestly.

## Queue 3: Video (third)
1. Accept normal/forwarded video and video-as-document; inspect input limits first.
2. Add a qualified, resource-bounded audio + sparse-frame timeline pipeline with
   a media worker gate separate from model admission. Validate Pi responsiveness.
3. Support summarization and timestamp questions; distinguish speech/frame/both
   evidence and missing intervals. Verify silent clips, audio-only inputs,
   scene changes, ASR uncertainty and Stop/restart under load.
4. Add subtitle/timeline exports, requested screenshots/clips and instructions/
   action lists as separate small follow-ups after the initial live gate.
5. Optional comparison and retained-media search come last, with explicit
   retention, authorization and source-location semantics.

Every implementation slice uses a feature worktree, updates live capability
state/HANDOFF, runs appropriate tests and independent review, then waits for CI
before merge. Each deployment records real evidence and remaining client checks.
No new external service or unattended analysis is authorized by this plan.
