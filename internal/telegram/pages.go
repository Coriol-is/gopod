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
	"unicode/utf8"

	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/control"
)

const (
	// pageMaxRunes is a page's size in markdown runes: below the
	// 4096-character message limit with room for the HTML rendering, so
	// a page is always one message.
	pageMaxRunes = 3000
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

// splitPages cuts text into pages of at most limit runes on line
// boundaries. A line longer than a page is cut by runes. A fenced code
// block cut by a boundary is closed at the end of its page and reopened
// (with its language) at the top of the next, so every page renders.
func splitPages(text string, limit int) []string {
	if utf8.RuneCountInString(text) <= limit {
		return []string{text}
	}
	const fence = "```"
	// Room for the closing fence a cut code block gets.
	room := limit - len("\n"+fence)
	var pages []string
	var cur []string
	curLen := 0
	inFence := false
	fenceOpen := ""
	flush := func() {
		page := strings.Join(cur, "\n")
		if inFence {
			page += "\n" + fence
		}
		pages = append(pages, page)
		cur, curLen = nil, 0
		if inFence {
			cur, curLen = []string{fenceOpen}, utf8.RuneCountInString(fenceOpen)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		for _, part := range cutRunes(line, room) {
			n := utf8.RuneCountInString(part)
			sep := 0
			if len(cur) > 0 {
				sep = 1
			}
			if len(cur) > 0 && curLen+sep+n > room {
				flush()
				if len(cur) > 0 {
					sep = 1
				} else {
					sep = 0
				}
			}
			cur = append(cur, part)
			curLen += sep + n
		}
		if t := strings.TrimSpace(line); strings.HasPrefix(t, fence) {
			inFence = !inFence
			if inFence {
				fenceOpen = t
			}
		}
	}
	if len(cur) > 0 {
		page := strings.Join(cur, "\n")
		pages = append(pages, page)
	}
	return pages
}

// cutRunes splits s into pieces of at most n runes ("" stays one piece).
func cutRunes(s string, n int) []string {
	if utf8.RuneCountInString(s) <= n {
		return []string{s}
	}
	var out []string
	r := []rune(s)
	for len(r) > n {
		out = append(out, string(r[:n]))
		r = r[n:]
	}
	return append(out, string(r))
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
	pages := splitPages(text, pageMaxRunes)
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
// to page n of its reply.
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
