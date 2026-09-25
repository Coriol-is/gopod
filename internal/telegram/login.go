package telegram

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/runner"
)

// loginTimeout is how long a /login session stays open waiting for
// the user to paste the OAuth code. After this, the exec is killed
// and the session is cleaned up.
const loginTimeout = 10 * time.Minute

// loginSession holds the state for one in-progress /login flow.
type loginSession struct {
	exec   *runner.InteractiveExec
	ctx    context.Context
	cancel context.CancelFunc
	chatID int64
	folder string

	// awaitsCode is true for the OAuth code flow (Claude), where the
	// user pastes a code back as their next message. The device flow
	// (Codex) completes in the browser and never reads a code, so its
	// session must not swallow the next message.
	awaitsCode bool
}

// loginSessions tracks per-chat login sessions. Protected by loginMu.
// Keyed by chatID (int64) not chatJID (string) because the Telegram
// handlers have chatID at hand and the mapping is 1:1.
type loginSessions struct {
	mu       sync.Mutex
	sessions map[int64]*loginSession
}

func newLoginSessions() *loginSessions {
	return &loginSessions{sessions: make(map[int64]*loginSession)}
}

func (ls *loginSessions) get(chatID int64) *loginSession {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return ls.sessions[chatID]
}

func (ls *loginSessions) set(chatID int64, s *loginSession) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	ls.sessions[chatID] = s
}

// awaitingCode returns the chat's login session if the given message
// should be treated as the pasted OAuth code, or nil if it should go
// through normal handling. Slash commands are never intercepted, so
// the user can still type /help mid-login.
func (ls *loginSessions) awaitingCode(chatID int64, text string) *loginSession {
	if strings.HasPrefix(text, "/") {
		return nil
	}
	s := ls.get(chatID)
	if s == nil || !s.awaitsCode {
		return nil
	}
	return s
}

func (ls *loginSessions) remove(chatID int64) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	delete(ls.sessions, chatID)
}

// loginHandlerReal is the M6.5 interactive OAuth proxy. It replaces
// the stub loginHandler when the runner is wired.
//
// Flow:
//
//  1. Ensure the chat's container is running.
//  2. Spawn `claude auth login` interactively (TTY + stdin).
//  3. Read stdout line by line until we find the URL.
//  4. Forward URL to the Telegram chat.
//  5. Tell the user to open it, login, copy the code, and send it back.
//  6. Store the loginSession so defaultHandler can intercept the code.
//  7. A background goroutine reads the rest of stdout and handles
//     timeout cleanup.
func (b *Bot) loginHandlerReal(ctx context.Context, _ *bot.Bot, update *models.Update) {
	if update == nil || update.Message == nil {
		return
	}
	m := update.Message
	b.persistMessage(ctx, m)

	if b.runner == nil {
		b.replyText(ctx, m.Chat.ID,
			"Runner is not available. Is Docker running?")
		return
	}

	rc, ok := b.resolveRegistered(ctx, m)
	if !ok {
		b.replyText(ctx, m.Chat.ID,
			"This chat is not registered. The owner can register it with /register.")
		return
	}

	// Refuse if already in a login session.
	if existing := b.logins.get(m.Chat.ID); existing != nil {
		b.replyText(ctx, m.Chat.ID,
			"A /login session is already active for this chat. "+
				"Paste the auth code from the browser, or wait for the session to time out.")
		return
	}

	tier := runner.TierRegistered
	if rc.IsOwner {
		tier = runner.TierOwner
	}

	// Ensure container is running.
	containerID, err := b.runner.Ensure(ctx, rc.Folder, tier, b.allowlist)
	if err != nil {
		b.log.Error("/login: ensure container failed",
			slog.String("folder", rc.Folder),
			slog.Any("err", err))
		b.replyText(ctx, m.Chat.ID, fmt.Sprintf("Failed to start container: %v", err))
		return
	}

	b.replyText(ctx, m.Chat.ID, "Starting authentication flow...")

	// Spawn interactive exec.
	loginCtx, loginCancel := context.WithTimeout(context.Background(), loginTimeout)
	// Use the per-chat provider (could be claude or codex).
	prov := b.runner.ProviderForChat(rc.Folder)
	exec, err := b.runner.Docker().ExecInteractive(loginCtx, containerID,
		prov.LoginCmd(),
		nil,
	)
	if err != nil {
		loginCancel()
		b.log.Error("/login: ExecInteractive failed",
			slog.String("folder", rc.Folder),
			slog.Any("err", err))
		b.replyText(ctx, m.Chat.ID, fmt.Sprintf("Failed to start login: %v", err))
		return
	}

	// Read lines looking for URL and optional device code.
	var url, deviceCode string
	for i := 0; i < 30; i++ {
		line, err := exec.ReadLine()
		if err != nil {
			if err == io.EOF {
				break
			}
			b.log.Error("/login: ReadLine failed",
				slog.Any("err", err))
			break
		}
		b.log.Debug("/login stdout", slog.String("line", line))
		extracted := prov.ExtractLoginURL(line)
		if extracted == "" {
			continue
		}
		if strings.HasPrefix(extracted, "CODE:") {
			deviceCode = strings.TrimPrefix(extracted, "CODE:")
		} else if url == "" {
			url = extracted
		}
		// If we have both URL and code (device auth), stop reading.
		if url != "" && deviceCode != "" {
			break
		}
		// For Claude-style auth (just URL, no code), stop after URL.
		if url != "" && prov.Name() == "claude" {
			break
		}
	}

	if url == "" {
		loginCancel()
		exec.DrainAndClose()
		b.replyText(ctx, m.Chat.ID,
			"Could not extract the login URL. Check gopod logs.")
		return
	}

	// Store the session.
	session := &loginSession{
		exec:       exec,
		ctx:        loginCtx,
		cancel:     loginCancel,
		chatID:     m.Chat.ID,
		folder:     rc.Folder,
		awaitsCode: deviceCode == "",
	}
	b.logins.set(m.Chat.ID, session)

	// Backticks make the URL an inline-code entity in Telegram:
	// copyable, and the markdown pass can't eat the underscores in
	// its query parameters (client_id, code_challenge, ...).
	var loginMsg string
	if deviceCode != "" {
		// Device auth flow (Codex): URL + one-time code.
		loginMsg = "Open this URL:\n\n`" + url + "`" +
			"\n\nEnter this code:\n\n**" + deviceCode + "**" +
			"\n\nAfter signing in, the bot will detect it automatically."
	} else {
		// OAuth code flow (Claude): URL + paste code back.
		loginMsg = "Open this URL to sign in:\n\n`" + url + "`" +
			"\n\nAfter signing in, copy the authorization code from the " +
			"success page and send it here as your next message."
	}
	b.replyText(ctx, m.Chat.ID, loginMsg)

	// Device flow: the CLI finishes on its own once the user signs in
	// in the browser. Wait for it in the background instead of leaving
	// the session open for the next message to fall into.
	if !session.awaitsCode {
		go b.finishLogin(ctx, m.Chat.ID, session)
	}

	// Background cleanup goroutine: if the timeout fires before
	// the user pastes the code, tear down the session.
	go func() {
		<-loginCtx.Done()
		if s := b.logins.get(m.Chat.ID); s == session {
			b.logins.remove(m.Chat.ID)
			exec.DrainAndClose()
			// Only notify if it was a timeout, not a clean completion.
			if loginCtx.Err() == context.DeadlineExceeded {
				b.replyText(context.Background(), m.Chat.ID,
					"Login session timed out. Run /login to try again.")
			}
		}
	}()
}

// handleLoginCode is called from defaultHandler when the chat has an
// active login session. It writes the user's message (the OAuth code)
// to claude's stdin and reads the result.
func (b *Bot) handleLoginCode(ctx context.Context, chatID int64, code string) {
	session := b.logins.get(chatID)
	if session == nil {
		return
	}

	b.replyText(ctx, chatID, "Verifying code...")

	// Write the code to claude's stdin.
	if _, err := session.exec.WriteStdin([]byte(code + "\n")); err != nil {
		b.log.Error("/login: WriteStdin failed",
			slog.Any("err", err))
		b.replyText(ctx, chatID, fmt.Sprintf("Failed to send code: %v", err))
		b.cleanupLoginSession(chatID, session)
		return
	}

	b.finishLogin(ctx, chatID, session)
}

// finishLogin reads the login CLI's output until it reports success or
// failure (or exits), tears the session down, and tells the user.
func (b *Bot) finishLogin(ctx context.Context, chatID int64, session *loginSession) {
	// Read lines looking for success or error indication.
	var resultLines []string
	for i := 0; i < 30; i++ {
		line, err := session.exec.ReadLine()
		if err != nil {
			if err == io.EOF {
				break
			}
			break
		}
		b.log.Debug("/login result", slog.String("line", line))
		cleaned := stripANSI(line)
		if cleaned != "" {
			resultLines = append(resultLines, cleaned)
		}
		// Check for known success patterns.
		lower := strings.ToLower(cleaned)
		if strings.Contains(lower, "logged in") ||
			strings.Contains(lower, "authenticated") ||
			strings.Contains(lower, "success") {
			break
		}
		// Check for known error patterns.
		if strings.Contains(lower, "error") ||
			strings.Contains(lower, "failed") ||
			strings.Contains(lower, "invalid") {
			break
		}
	}

	// Timed out: the timeout goroutine owns the user-facing message.
	if session.ctx != nil && session.ctx.Err() == context.DeadlineExceeded {
		return
	}
	b.cleanupLoginSession(chatID, session)

	provName := "the agent"
	if b.runner != nil {
		provName = b.runner.ProviderForChat(session.folder).Name()
	}

	if len(resultLines) == 0 {
		// Check auth status as fallback.
		if b.checkAuthAfterLogin(ctx, session) {
			b.replyText(ctx, chatID, "Logged in successfully! Send a message to start chatting with "+provName+".")
		} else {
			b.replyText(ctx, chatID,
				"Login completed but could not verify auth status. "+
					"Try sending a message — if it fails, run /login again.")
		}
		return
	}

	result := strings.Join(resultLines, "\n")
	lower := strings.ToLower(result)
	if strings.Contains(lower, "logged in") ||
		strings.Contains(lower, "authenticated") ||
		strings.Contains(lower, "success") {
		b.replyText(ctx, chatID, result+"\n\nSend a message to start chatting with "+provName+".")
	} else {
		b.replyText(ctx, chatID, "Login result:\n\n"+result)
	}
}

// checkAuthAfterLogin does a quick `claude auth status --json` poll
// to verify the login succeeded even if stdout was ambiguous.
func (b *Bot) checkAuthAfterLogin(ctx context.Context, session *loginSession) bool {
	if b.runner == nil {
		return false
	}
	tier := runner.TierRegistered
	status, err := b.runner.CheckAuth(ctx, session.folder, tier, b.allowlist)
	if err != nil {
		return false
	}
	return status.LoggedIn
}

func (b *Bot) cleanupLoginSession(chatID int64, session *loginSession) {
	session.cancel()
	session.exec.DrainAndClose()
	b.logins.remove(chatID)
}

// extractURL finds the URL in the "If the browser didn't open, visit: <URL>"
// line that claude auth login prints.
func extractURL(line string) string {
	const prefix = "visit: "
	idx := strings.Index(strings.ToLower(line), prefix)
	if idx < 0 {
		// Also try just finding https://claude.com directly.
		if i := strings.Index(line, "https://"); i >= 0 {
			url := line[i:]
			// Trim trailing whitespace or ANSI.
			url = strings.TrimRight(url, " \t\r\n")
			return url
		}
		return ""
	}
	url := strings.TrimSpace(line[idx+len(prefix):])
	return url
}

// stripANSI removes common ANSI escape sequences from terminal output.
// Not exhaustive — covers CSI sequences (ESC[...X) which is what TTY
// output from claude typically contains (cursor movement, colors).
func stripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			// Skip until the terminating letter.
			j := i + 2
			for j < len(s) && !isCSITerminator(s[j]) {
				j++
			}
			if j < len(s) {
				j++ // skip the terminator itself
			}
			i = j
			continue
		}
		if s[i] == '\r' {
			i++
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return strings.TrimSpace(b.String())
}

func isCSITerminator(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}
