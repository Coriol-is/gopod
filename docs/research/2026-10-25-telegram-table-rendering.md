# Telegram table rendering — code and API research

Date: 2026-10-25. Status: the monospace `<pre>` table with a row-card fallback for wide tables shipped
(D050, 2026-10-30); native Rich Tables remain a separate spike, not implemented.
This is separate from CP-017 capability diagnostics.

## Finding and recommended direction

A deterministic presentation filter is useful. The immediate recommendation is
bounded Markdown-table parsing with a lossless row-card fallback. A separate
client spike should evaluate Telegram's native Rich Tables before choosing a
native-table default. Prompt guidance can reduce unwanted tables but cannot
replace output formatting for provider responses, IPC and imported text.

The issue is not that Telegram universally lacks tables. Ordinary
`sendMessage(parse_mode=HTML)` does not accept `<table>`, while the current Bot
API supports tables through Rich Messages. `sendRichMessage` accepts
`InputRichMessage` with exactly one of Markdown, HTML or explicit blocks.
`InputRichBlockTable` supports cells and compact/bordered/striped presentation.
Rich-message limits include 32768 UTF-8 characters, 500 blocks and 20 table
columns; table cells allow inline formatting only. These API facts were checked
against the official documentation on the date above:

- [Ordinary message formatting](https://core.telegram.org/bots/api#formatting-options)
- [Rich message formatting and limits](https://core.telegram.org/bots/api#rich-message-formatting-options)
- [InputRichMessage](https://core.telegram.org/bots/api#inputrichmessage)
- [InputRichBlockTable](https://core.telegram.org/bots/api#inputrichblocktable)
- [sendRichMessage](https://core.telegram.org/bots/api#sendrichmessage)
- [sendRichMessageDraft](https://core.telegram.org/bots/api#sendrichmessagedraft)

Native-table mobile layout, old-client behavior and actual bot delivery were
not tested. API support and SDK types do not prove satisfactory rendering on
any particular Telegram client.

## Current repository evidence

Code inspected at source base `2cdb82d` (the table-research worktree base):

- [format.go](../../internal/telegram/format.go): `renderMarkdown` is a line-based
  renderer with fenced code blocks and inline emphasis/code handling. There is
  no Markdown-table parser. Pipes and separator rows remain text. The comment
  advertises Markdown links becoming anchors, but the implementation does not
  parse links; a table feature must not assume existing link preservation.
- [handler.go](../../internal/telegram/handler.go): `sendRenderedOpts` handles
  topic fallback, then `sendChunks` renders HTML and splits at 4000 HTML bytes
  with `chunkHTML`. Rejected markup can fall back to plain text.
- [pages.go](../../internal/telegram/pages.go): command replies are paginated
  before sending; `pageFits` measures rendered HTML with a 3800-byte budget.
  `splitPages` understands fences, not table rows or row-card boundaries.
- [chunkhtml.go](../../internal/telegram/chunkhtml.go): splitting preserves HTML
  tags, entities and UTF-8 validity. That does not make tabular data readable.
- [streaming.go](../../internal/telegram/streaming.go): ordinary previews use
  plain-text drafts/edits. Final replies can use `sendFinal`, or edit an existing
  placeholder directly with `finalReplyHTML`; Stop/partial completion has its
  own final edit path. Updating only `sendFinal` would miss output paths.
- [longcode.go](../../internal/telegram/longcode.go): `attachLongCode` provides an
  existing example of attaching oversized content, with explicit delivery-failure
  behavior. [document.go](../../internal/telegram/document.go) can send an in-memory
  document to a thread. Reusing these requires preserving their delivery scope
  and acknowledging duplicate-delivery windows.
- [go.mod](../../go.mod) pins `github.com/go-telegram/bot v1.27.0`. The locally
  inspected module already has `SendRichMessage`, `SendRichMessageDraft`,
  `models.InputRichMessage`, `models.InputRichBlockTable` and
  `models.RichBlockTableCell`. A dependency upgrade is not required just to
  prototype native tables. Relevant upstream SDK sources:
  [methods](https://github.com/go-telegram/bot/blob/v1.27.0/methods.go),
  [rich message models](https://github.com/go-telegram/bot/blob/v1.27.0/models/rich_message.go),
  [input block models](https://github.com/go-telegram/bot/blob/v1.27.0/models/input_rich_block.go).

No source changes or live Telegram calls were made for this analysis.

## Proposed presentation contract

Introduce a small pure parser producing prose, code and table blocks. Keep it
bounded; a full general document-rendering framework is unnecessary for the
first slice. Avoid one regular expression or `strings.Split(line, "|")`.

Recognize a table only after a valid header and delimiter row, with optional
outer pipes and alignment markers. Preserve cell order, empty values, duplicate
headers and all source values. The parser must track backslash escapes, inline
code delimiter runs, fenced/indented code, and link syntax so pipes in code or
URLs are not blindly treated as column separators. If the supported dialect
cannot resolve an ambiguous construct, preserve that block as original text.
Support for optional indentation and fence styles must be explicit, tested and
consistent with the surrounding Markdown scanner.

Use maximum input size, row/cell counts and linear scanning to bound work. Do
not repeatedly reparse the complete growing output on each streamed token.
Transform completed output; incomplete preview tables can remain plain text.

Select a presentation from parsed data:

| Presentation | Suitable use | Tradeoff |
|---|---|---|
| Row cards / labeled lists | Default fallback; long text, links, mobile reading | Repeated labels add vertical length |
| Monospaced text | Explicit request; short numeric tables with few columns | Client width and Unicode display width make alignment fragile; links are less useful inside code |
| Native Rich Table | Separate verified transport option | Needs client tests and send/edit/fallback integration |
| CSV or HTML document | Many rows/columns; data export requested | Additional delivery and file lifecycle; not an inline reading solution |
| Image | Explicit presentation/export request | No clickable links, copy/search or accessible cell text; wide/long images remain difficult |

For example, a source row with name, status and source link becomes:

```text
Codex
Status: active
Source: docs — https://example.org
```

Use the first suitable column as the card title; include all other columns as
labeled fields. Empty/duplicate headings get deterministic labels such as
`Column 2` instead of dropping data. Preserve link targets explicitly, even
when the surrounding Markdown renderer cannot yet render clickable anchors.
Do not use another model call to summarize cells or silently truncate rows.
Original tables remain available in source history/export.

Monospaced alignment requires display-column width rather than byte or rune
counts; emoji, East Asian characters and combining sequences are counterexamples.
Even correct width calculation cannot guarantee a usable phone viewport. Keep
this an explicit option rather than the universal fallback.

## Integration seams and delivery constraints

Normalize before HTML escaping/inline rendering, and before command pagination
measures/splits the result. One preparation helper should cover the ordinary
Markdown renderer, final-reply renderer and command-page preparation. Make the
transformation idempotent so accidental repeated preparation cannot duplicate
labels. Do not modify provider/native transcripts or conversation history just
to adapt Telegram presentation; retain original source for Copy/Quote/export
and decide each action's presentation deliberately.

Row cards should supply semantic split boundaries where practical. Avoid placing
a card title alone on one page and its fields on the next. When a single card
cannot fit, continue with its title/continuation label. Continue measuring the
actual escaped HTML byte budget used by the current pipeline; raw Markdown
character counts are insufficient. Keep balanced markup, all values and links.

Native Rich Tables are a transport change, not an extra `<table>` branch in the
existing ordinary-message renderer. Prefer constructing validated blocks/cells
from parsed content to passing arbitrary model-generated rich HTML/Markdown
through unchanged. Rich syntax exposes additional constructs, including media,
which the current escaped renderer does not execute. A table feature should not
silently enable external media fetches or new interaction modes.

Preserve chat/thread/reply targets, action rows, recorded reply IDs, Stop behavior
and topic-gone handling on both final-send and final-edit paths. Keep ordinary
conversational output public in groups; private service delivery remains CP-019.
Do not add table-specific fallback to a different recipient or chat.

Only a definite API rejection before delivery permits an immediate alternate
presentation. Timeout/transport ambiguity can mean that the rich message was
already sent; sending row cards then can duplicate the response. Use bounded
429 handling and the existing delivery evidence boundaries rather than claiming
exactly-once fallback.

For document export, reuse scoped in-memory upload where suitable. Preserve a
useful inline explanation when upload fails. CSV opened in spreadsheet software
needs a policy for formula-like cells; an HTML export must escape content and
must not introduce scripts/external resources. Neither should silently replace
an unbounded table with an incomplete sample.

## Suggested implementation slices

1. **Reliable fallback:** parser, row-card presentation, explicit URL preservation,
   shared preparation seam, size/paging coverage and real-client acceptance.
   Add short model guidance preferring readable lists for Telegram as a secondary
   measure. Do not implement native transport or automatic exports in this slice.
2. **Native-table spike:** exercise a small table in private and group contexts on
   current Desktop/Android/iOS; inspect forwarding, copy, replies, edits, thread
   identity, actions and API refusal behavior. Test larger cells and columns.
   Document tested client versions; do not infer compatibility from API success.
3. **Large-table exports:** define a clear threshold/request policy, summary plus
   full document, and delivery failure behavior. Images remain request-driven.

These are recommendations, not accepted implementation scope or an active
release. They should be tracked separately from CP-017.

## Verification matrix

- Header/delimiter recognition, optional pipes, alignment markers, empty cells,
  duplicate/empty headings and inconsistent row lengths.
- Escaped pipes, variable-length backtick code spans, fenced/indented code,
  URLs containing pipes/parentheses/underscores, adjacent tables and mixed prose.
- Malformed and partially streamed tables retain safe source text; no unrelated
  ordinary pipe-delimited sentence becomes a table.
- Cyrillic, CJK, emoji, combining text, RTL and HTML-sensitive values; injection
  strings stay data. Every source cell and URL survives the transformation.
- Large inputs/cell counts stay within bounded time/memory; no quadratic parsing.
- Rendered HTML limits, no split entities/UTF-8/tags, semantic card continuation,
  source text unchanged and normalization idempotent.
- Complete final send, in-place final edit, Stop/partial reply, command paging,
  copy/quote/export and topic fallback all exercise the same declared policy.
- Native transport tests cover explicit API rejection, 429, ambiguous timeout,
  edit/reply IDs and zero unapproved media fetches. No fallback duplicates claimed.
- Real client checks distinguish observed readability from fake-API correctness.
