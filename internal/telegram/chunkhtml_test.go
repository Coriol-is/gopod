package telegram

import (
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

var anyTag = regexp.MustCompile(`<[^>]*>`)

// checkChunks fails t unless every chunk is at most n bytes, valid
// UTF-8, has balanced tags, cuts no entity, and the chunks carry exactly
// the text of src (tags aside).
func checkChunks(t *testing.T, src string, chunks []string, n int) {
	t.Helper()
	var text strings.Builder
	for i, c := range chunks {
		if len(c) > n {
			t.Errorf("chunk %d is %d bytes, limit %d", i, len(c), n)
		}
		if !utf8.ValidString(c) {
			t.Errorf("chunk %d is not valid UTF-8: %q", i, c)
		}
		if err := balanced(c); err != nil {
			t.Errorf("chunk %d: %v\n%q", i, err, c)
		}
		plain := anyTag.ReplaceAllString(c, "")
		if strings.Count(plain, "&") != strings.Count(plain, ";") {
			t.Errorf("chunk %d cuts an entity: %q", i, plain)
		}
		text.WriteString(plain)
	}
	if want := anyTag.ReplaceAllString(src, ""); text.String() != want {
		t.Errorf("chunks lost or changed text\ngot:  %q\nwant: %q", text.String(), want)
	}
}

// balanced reports an error unless every tag in s is closed in order.
func balanced(s string) error {
	var stack []string
	for _, tag := range anyTag.FindAllString(s, -1) {
		if strings.HasPrefix(tag, "</") {
			name := tag[2 : len(tag)-1]
			if len(stack) == 0 || stack[len(stack)-1] != name {
				return fmt.Errorf("unexpected %s with open %v", tag, stack)
			}
			stack = stack[:len(stack)-1]
			continue
		}
		name := strings.Fields(strings.Trim(tag, "<>"))[0]
		stack = append(stack, name)
	}
	if len(stack) > 0 {
		return fmt.Errorf("unclosed %v", stack)
	}
	return nil
}

func TestChunkHTML_ShortIsOneChunk(t *testing.T) {
	in := "<b>hi</b> there"
	if got := chunkHTML(in, 100); len(got) != 1 || got[0] != in {
		t.Errorf("chunkHTML = %q", got)
	}
}

func TestChunkHTML_QuotedPreReopensAcrossChunks(t *testing.T) {
	var md strings.Builder
	md.WriteString("Here is the file:\n```go\n")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&md, "fmt.Println(\"line %02d\", a < b && c > d)\n", i)
	}
	md.WriteString("```\nDone.")
	src := finalReplyHTML(md.String())
	const n = 400
	chunks := chunkHTML(src, n)
	if len(chunks) < 3 {
		t.Fatalf("want several chunks, got %d", len(chunks))
	}
	checkChunks(t, src, chunks, n)
	for i, c := range chunks[1 : len(chunks)-1] {
		if !strings.HasPrefix(c, "<blockquote expandable><pre>") || !strings.HasSuffix(c, "</pre></blockquote>") {
			t.Errorf("middle chunk %d not wrapped in quote+pre: %q", i+1, c)
		}
	}
	if last := chunks[len(chunks)-1]; strings.Contains(last, "<pre></pre>") {
		t.Errorf("last chunk reopens an empty pre: %q", last)
	}
}

func TestChunkHTML_CodeSpansAndLinks(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&sb, `use <code>go test -run X%d ./...</code> and <a href="https://e.com/?q=a b&amp;n=%d">the docs page %d</a> `, i, i, i)
	}
	src := sb.String() // one long line: only spaces to split on, many inside elements
	for _, n := range []int{60, 97, 150, 400} {
		chunks := chunkHTML(src, n)
		checkChunks(t, src, chunks, n)
		for i, c := range chunks {
			if strings.Contains(c, `<a href="https://e.com/?q=a`) && !strings.Contains(c, `n=`) {
				t.Errorf("n=%d chunk %d cuts inside an <a> tag: %q", n, i, c)
			}
		}
	}
}

func TestChunkHTML_EntitiesAndRunes(t *testing.T) {
	src := strings.Repeat("&amp;&lt;", 200) + strings.Repeat("привет", 200)
	for _, n := range []int{7, 33, 100} {
		checkChunks(t, src, chunkHTML(src, n), n)
	}
}

func TestChunkHTML_RandomMarkdown(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	words := []string{"alpha", "**bold**", "`a b`", "_it_", "x<y", "&", "ünï", "\n", "\n\n", "```py\nprint(1)\nprint(2)\n```"}
	for iter := 0; iter < 300; iter++ {
		var md strings.Builder
		for k := rng.Intn(400); k > 0; k-- {
			md.WriteString(words[rng.Intn(len(words))])
			md.WriteString(" ")
		}
		if rng.Intn(2) == 0 {
			md.WriteString("\n```go\n")
			for k := rng.Intn(80); k > 0; k-- {
				md.WriteString("x := a && b <- c\n")
			}
		}
		src := finalReplyHTML(md.String())
		n := 120 + rng.Intn(300) // room for quote+pre open and close tags (47 bytes)
		checkChunks(t, src, chunkHTML(src, n), n)
		if t.Failed() {
			t.Fatalf("iteration %d, n=%d, md=%q", iter, n, md.String())
		}
	}
}
