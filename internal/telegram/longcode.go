package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// A fenced code block in the agent's final reply larger than these is
// sent as a document instead of inline (finalReplyHTML collapses the
// medium ones).
const (
	docOverLines = 60
	docOverChars = 3000
)

// codeDoc is one fenced block taken out of a reply to go as a document.
type codeDoc struct {
	Name  string // snippet.go, snippet-2.py, ...
	Lines int    // lines of code
	Body  string // the file content
	block string // the fenced block as written, fences included
}

// note is the line that stands in the reply for a sent document.
func (d codeDoc) note() string {
	return fmt.Sprintf("📎 %s (%s)", d.Name, plural(d.Lines, "line"))
}

// caption is the document's caption: short, the file name is shown anyway.
func (d codeDoc) caption() string {
	return plural(d.Lines, "line")
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// longCodeReply is a reply split around its oversized code blocks.
// lines holds the reply line by line; an entry with doc >= 0 stands for
// the whole fenced block docs[doc].
type longCodeReply struct {
	lines []replyLine
	docs  []codeDoc
}

type replyLine struct {
	text string
	doc  int // -1: a line of text
}

// splitLongCode finds the fenced blocks of md over docOverLines lines or
// docOverChars characters, with the same fence rules as renderMarkdown
// (a fence never closed runs to the end, as in a stopped turn's partial
// text). Pure: nothing is sent.
func splitLongCode(md string) longCodeReply {
	var r longCodeReply
	lines := strings.Split(md, "\n")
	for i := 0; i < len(lines); i++ {
		if !isFence(lines[i]) {
			r.lines = append(r.lines, replyLine{text: lines[i], doc: -1})
			continue
		}
		end := i + 1
		for end < len(lines) && !isFence(lines[end]) {
			end++
		}
		blockEnd := min(end+1, len(lines)) // closing fence included when present
		body := strings.Join(lines[i+1:end], "\n")
		if n := end - (i + 1); n > docOverLines || len(body) > docOverChars {
			d := codeDoc{
				Name:  snippetName(fenceLang(lines[i]), len(r.docs)+1),
				Lines: n,
				Body:  body + "\n",
				block: strings.Join(lines[i:blockEnd], "\n"),
			}
			r.lines = append(r.lines, replyLine{doc: len(r.docs)})
			r.docs = append(r.docs, d)
		} else {
			for _, l := range lines[i:blockEnd] {
				r.lines = append(r.lines, replyLine{text: l, doc: -1})
			}
		}
		i = blockEnd - 1
	}
	return r
}

// text renders the reply: a block whose document was sent becomes its
// note, any other block stays inline as written.
func (r longCodeReply) text(sent []bool) string {
	out := make([]string, len(r.lines))
	for i, l := range r.lines {
		switch {
		case l.doc < 0:
			out[i] = l.text
		case sent[l.doc]:
			out[i] = r.docs[l.doc].note()
		default:
			out[i] = r.docs[l.doc].block
		}
	}
	return strings.Join(out, "\n")
}

// fenceLang returns the language tag of an opening fence, lowercased.
func fenceLang(line string) string {
	f := strings.Fields(strings.TrimPrefix(line, "```"))
	if len(f) == 0 {
		return ""
	}
	return strings.ToLower(strings.Trim(f[0], "{}."))
}

// langExt maps fence languages to file extensions; anything else is .txt.
var langExt = map[string]string{
	"go": ".go", "golang": ".go",
	"python": ".py", "py": ".py",
	"javascript": ".js", "js": ".js", "jsx": ".jsx", "mjs": ".js",
	"typescript": ".ts", "ts": ".ts", "tsx": ".tsx",
	"sh": ".sh", "bash": ".sh", "shell": ".sh", "zsh": ".sh", "console": ".sh",
	"json": ".json", "yaml": ".yaml", "yml": ".yaml", "toml": ".toml",
	"ini": ".ini", "xml": ".xml", "html": ".html", "css": ".css",
	"sql": ".sql", "md": ".md", "markdown": ".md",
	"rust": ".rs", "rs": ".rs", "java": ".java", "kotlin": ".kt", "kt": ".kt",
	"c": ".c", "h": ".h", "cpp": ".cpp", "c++": ".cpp", "cs": ".cs", "csharp": ".cs",
	"ruby": ".rb", "rb": ".rb", "php": ".php", "swift": ".swift", "lua": ".lua",
	"diff": ".diff", "patch": ".diff", "dockerfile": ".dockerfile",
	"makefile": ".mk", "make": ".mk", "proto": ".proto", "hcl": ".tf", "terraform": ".tf",
}

// snippetName names the n-th document of a reply (1-based): snippet.go,
// then snippet-2.py, snippet-3.go, so names never collide.
func snippetName(lang string, n int) string {
	ext, ok := langExt[lang]
	if !ok {
		ext = ".txt"
	}
	if n == 1 {
		return "snippet" + ext
	}
	return fmt.Sprintf("snippet-%d%s", n, ext)
}

// attachLongCode sends each oversized code block of a final reply as a
// document into thread (main when the topic is gone) and returns the
// reply text to send, with those blocks replaced by a one-line note. A
// block whose document fails stays inline, so the code is never lost
// (finalReplyHTML collapses it). Documents go before the text, which is
// the turn's reply; if the text then fails and the queue retries the
// turn, the documents are sent again.
func (b *Bot) attachLongCode(ctx context.Context, chatID int64, thread int, text string) string {
	r := splitLongCode(text)
	if len(r.docs) == 0 {
		return text
	}
	sent := make([]bool, len(r.docs))
	for i, d := range r.docs {
		if _, err := b.sendDocument(ctx, chatID, thread, d.Name, []byte(d.Body), d.caption()); err != nil {
			b.log.Warn("code document not sent, keeping it inline",
				slog.Int64("chat_id", chatID),
				slog.String("name", d.Name),
				slog.Any("err", err))
			continue
		}
		sent[i] = true
	}
	return r.text(sent)
}

// stripCodeBlocks removes the fenced code blocks from text, fences
// included (an unclosed fence runs to the end), for the voice reply.
func stripCodeBlocks(text string) string {
	var out []string
	inBlock := false
	for _, l := range strings.Split(text, "\n") {
		if isFence(l) {
			inBlock = !inBlock
			continue
		}
		if !inBlock {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
