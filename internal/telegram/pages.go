package telegram

// Paged command replies (spec F9, D028): a command reply longer than one
// message (/logs, /recall, /tasks list, /help) is sent as one message
// with ‹ n/N › buttons instead of several chunks. The pages stay in
// memory for pagesTTL under a random token; a press (pg:<token>:<n>)
// edits the same message to page n. A restart or the TTL forgets them:
// the press toasts that the command should be run again.

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/control"
)

const (
	// pageMaxHTMLBytes is a page's size limit measured as what is sent:
	// the HTML rendering, in bytes. sendChunks splits at 4000 bytes of
	// HTML, so a page under this always goes out as one message, also
	// with the topic-gone note (threadGoneNote) in front of it. Cyrillic
	// takes 2 bytes a character and &, <, > and quotes grow when escaped,
	// which is why a count of markdown characters is not enough.
	pageMaxHTMLBytes = 3800
	// pagesTTL is how long the page buttons of a reply work.
	pagesTTL = 10 * time.Minute
)

// pagedReply is a paged command reply: its pages and the command
// buttons shown under every page.
type pagedReply struct {
	chatID  int64
	pages   []string
	rows    [][]models.InlineKeyboardButton
	expires time.Time
}

// pageFits reports whether a page of markdown is one message once
// rendered to HTML.
func pageFits(md string) bool {
	return len(markdownToTelegramHTML(md)) <= pageMaxHTMLBytes
}

// withLine returns lines plus line, never writing into lines' array.
func withLine(lines []string, line string) []string {
	out := make([]string, len(lines), len(lines)+1)
	copy(out, lines)
	return append(out, line)
}

// splitPages cuts text into pages that each fit (fits), on line
// boundaries. A line longer than a page is cut by runes. A fenced code
// block cut by a boundary is closed at the end of its page and reopened
// (with its language) at the top of the next, so every page renders; a
// page never ends with a lone fence opener (it moves to the next page),
// and a reopened block is never emitted empty.
func splitPages(text string, fits func(string) bool) []string {
	if fits(text) {
		return []string{text}
	}
	const fence = "```"
	render := func(lines []string, open bool) string {
		s := strings.Join(lines, "\n")
		if open {
			s += "\n" + fence
		}
		return s
	}
	var pages []string
	var cur []string
	inFence := false
	fenceOpen := ""
	openerLast := false // cur ends with the opener of the current block
	fresh := false      // cur holds only a reopened opener
	flush := func() {
		if openerLast {
			// Do not end a page with an opener: it starts the next one.
			if rest := cur[:len(cur)-1]; len(rest) > 0 {
				pages = append(pages, render(rest, false))
			}
			cur, openerLast, fresh = []string{fenceOpen}, true, false
			return
		}
		pages = append(pages, render(cur, inFence))
		cur, fresh = nil, false
		if inFence {
			cur, fresh = []string{fenceOpen}, true
		}
	}
	for _, line := range strings.Split(text, "\n") {
		isFence := isFence(line)
		after := inFence != isFence
		if len(cur) > 0 && !fresh && !fits(render(withLine(cur, line), after)) {
			flush()
		}
		rest := line
		for !fits(render(withLine(cur, rest), after)) {
			// rest does not fit even on this page: cut the longest prefix
			// that does and start a new page.
			n := longestFit(rest, func(p string) bool { return fits(render(withLine(cur, p), inFence)) })
			r := []rune(rest)
			cur = append(cur, string(r[:n]))
			openerLast, fresh = false, false
			rest = string(r[n:])
			flush()
		}
		cur = append(cur, rest)
		openerLast = isFence && after
		fresh = false
		if isFence {
			inFence = after
			if after {
				fenceOpen = strings.TrimSpace(line)
			}
		}
	}
	if len(cur) > 0 {
		pages = append(pages, render(cur, false))
	}
	return pages
}

// longestFit returns the largest rune count n ≥ 1 of s whose prefix
// satisfies ok (1 when none does, so cutting always progresses).
func longestFit(s string, ok func(string) bool) int {
	r := []rune(s)
	lo, hi := 1, len(r)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if ok(string(r[:mid])) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

// pageRow is the ‹ n/N › row of page (0-based) of total. The counter
// re-shows the current page; ‹ and › exist only where there is a page.
func pageRow(tok string, page, total int) []models.InlineKeyboardButton {
	data := func(n int) string {
		d, _ := encodeCB("pg", tok, strconv.Itoa(n)) // ≤ 24 bytes
		return d
	}
	var row []models.InlineKeyboardButton
	if page > 0 {
		row = append(row, models.InlineKeyboardButton{Text: btnPagePrev, CallbackData: data(page - 1)})
	}
	row = append(row, models.InlineKeyboardButton{
		Text:         strconv.Itoa(page+1) + "/" + strconv.Itoa(total),
		CallbackData: data(page),
	})
	if page < total-1 {
		row = append(row, models.InlineKeyboardButton{Text: btnPageNext, CallbackData: data(page + 1)})
	}
	return row
}

// pageMarkup is the keyboard of one page: the page row, then the
// reply's command buttons.
func pageMarkup(tok string, page int, p pagedReply) models.ReplyMarkup {
	rows := append([][]models.InlineKeyboardButton{pageRow(tok, page, len(p.pages))}, p.rows...)
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// commandMessage renders a command Response for chatID as the text of
// one message and its keyboard: the first page and the page row when
// the text needs several messages, then the Response's buttons.
func (b *Bot) commandMessage(chatID int64, resp control.Response) (string, models.ReplyMarkup) {
	text := resp.Text
	if text == "" {
		text = cmdTextDone
	}
	rows := b.commandRows(chatID, resp.Buttons)
	pages := splitPages(text, pageFits)
	if len(pages) == 1 {
		if len(rows) == 0 {
			return text, nil
		}
		return text, &models.InlineKeyboardMarkup{InlineKeyboard: rows}
	}
	now := b.clock()
	p := pagedReply{chatID: chatID, pages: pages, rows: rows, expires: now.Add(pagesTTL)}
	tok := newButtonToken()
	b.btnMu.Lock()
	b.pruneButtonsLocked(now)
	if b.pages == nil {
		b.pages = make(map[string]pagedReply)
	}
	b.pages[tok] = p
	b.btnMu.Unlock()
	return pages[0], pageMarkup(tok, 0, p)
}

// pageButton handles ‹ › and n/N (pg:<token>:<n>): it edits the message
// to page n of its reply. Pressing the counter re-sends the page shown;
// Telegram's "message is not modified" is only logged.
func (b *Bot) pageButton(ctx context.Context, q cbQuery) (string, error) {
	if len(q.Data.Args) != 2 {
		return pagesToastExpired, nil
	}
	tok := q.Data.Args[0]
	n, err := strconv.Atoi(q.Data.Args[1])
	if err != nil {
		return pagesToastExpired, nil
	}
	now := b.clock()
	b.btnMu.Lock()
	p, ok := b.pages[tok]
	b.btnMu.Unlock()
	if !ok || now.After(p.expires) || n < 0 || n >= len(p.pages) {
		return pagesToastExpired, nil
	}
	if p.chatID != q.ChatID {
		return cbToastNotYours, nil
	}
	b.editCommandText(ctx, q.ChatID, q.MessageID, p.pages[n], pageMarkup(tok, n, p))
	return "", nil
}
