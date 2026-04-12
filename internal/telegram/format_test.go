package telegram

import "testing"

func TestMarkdownToTelegramHTML(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain text", "hello world", "hello world"},
		{"bold", "this is **bold** text", "this is <b>bold</b> text"},
		{"italic", "this is *italic* text", "this is <i>italic</i> text"},
		{"inline code", "use `fmt.Println`", "use <code>fmt.Println</code>"},
		{"code block", "```go\nfmt.Println(\"hi\")\n```", "<pre>fmt.Println(&#34;hi&#34;)\n</pre>"},
		{"html escape", "x < y && z > w", "x &lt; y &amp;&amp; z &gt; w"},
		{"bold + inline code", "**bold** and `code`", "<b>bold</b> and <code>code</code>"},
		{"unclosed code block", "```\nhello", "<pre>hello</pre>"},
		{"multiple bold", "**a** and **b**", "<b>a</b> and <b>b</b>"},
		{"no markdown", "just plain", "just plain"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := markdownToTelegramHTML(c.in)
			if got != c.want {
				t.Errorf("\ngot:  %q\nwant: %q", got, c.want)
			}
		})
	}
}
