package telegram

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
)

// loginTimeout is how long a /login session stays open waiting for
// the user to paste the OAuth code. Timeout closes its attachment and
// cleans the session; this is not proof the CLI process was terminated.
const loginTimeout = 10 * time.Minute

const loginPrivateToast = "Sign in from private chat"

// loginIO keeps raw provider output confined to the login flow.
type loginIO interface {
	ReadLine() (string, error)
	WriteStdin([]byte) (int, error)
	Close() error
}

func privateLoginMessage(m *models.Message) bool {
	return m != nil && m.Chat.Type == models.ChatTypePrivate && m.Chat.ID > 0
}

// loginSession holds the state for one in-progress /login flow.
type loginSession struct {
	exec   loginIO
	ctx    context.Context
	cancel context.CancelFunc
	chatID int64
	folder string
	// thread is the private-chat topic /login was sent in (0 = main
	// chat). The session stays per chat; every message of the flow
	// answers in this topic.
	thread int

	// awaitsCode is true for the OAuth code flow (Claude), where the
	// user pastes a code back as their next message. The device flow
	// (Codex) completes in the browser and never reads a code, so its
	// session must not swallow the next message.
	awaitsCode bool
	private    bool
	actorID    int64
	tier       runner.Tier
	provider   string
	loginURL   string
	deviceCode string
}

// loginSessions tracks per-chat login sessions. Protected by loginMu.
// Keyed by chatID (int64) not chatJID (string) because the Telegram
// handlers have chatID at hand and the mapping is 1:1.
type loginSessions struct {
	mu       sync.Mutex
	sessions map[int64]*loginSession
	// starting marks chats whose login is being started (reserve):
	// between the reservation and set (or release) no second login
	// may start, so two quick /login or 🔑 presses run one exec.
	starting map[int64]bool
}

// reserve claims chatID's login slot. False when a login session is
// active or another start holds the slot. The holder ends the claim
// with set (session stored) or release (start failed).
func (ls *loginSessions) reserve(chatID int64) bool {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if ls.sessions[chatID] != nil || ls.starting[chatID] {
		return false
	}
	if ls.starting == nil {
		ls.starting = make(map[int64]bool)
	}
	ls.starting[chatID] = true
	return true
}

// isStarting reports whether chatID's login slot is reserved by a start
// that has not stored its session yet.
func (ls *loginSessions) isStarting(chatID int64) bool {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return ls.starting[chatID]
}

// release gives up a reservation whose login did not start.
func (ls *loginSessions) release(chatID int64) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	delete(ls.starting, chatID)
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
	delete(ls.starting, chatID) // the reservation becomes the session
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
	if !privateLoginMessage(m) {
		b.requestLoginHandoff(ctx, m)
		return
	}
	b.startLogin(ctx, m, inboundThreadID(m))
}

// startLogin runs the /login flow for the chat of m, answering in
// thread. The /login command and the login button (actions.go) share
// it; m is the command message or the message carrying the button.
func (b *Bot) startLogin(ctx context.Context, m *models.Message, thread int) {
	if !privateLoginMessage(m) {
		if m != nil {
			b.requestLoginHandoff(ctx, m)
		}
		return
	}
	rc, ok := b.resolveRegistered(ctx, m)
	if !ok {
		b.replyLoginText(ctx, m.Chat.ID, thread,
			"This chat is not registered. The owner can register it with /register.")
		return
	}
	// Refuse if a login is active or being started: claimed atomically
	// before the slow container start, so two quick presses run one.
	if !b.logins.reserve(m.Chat.ID) {
		text := loginTextAlready
		if b.logins.isStarting(m.Chat.ID) {
			text = loginTextStarting
		}
		b.replyLoginText(ctx, m.Chat.ID, thread, text)
		return
	}
	b.loginReserved(ctx, m, thread, rc)
}

// loginReserved runs the login flow for a chat whose login slot the
// caller reserved, and releases the slot when no session was stored.
func (b *Bot) loginReserved(ctx context.Context, m *models.Message, thread int, rc store.RegisteredChat) {
	if !privateLoginMessage(m) {
		if m != nil {
			b.cancelUnsafeLogin(m.Chat.ID)
		}
		return
	}
	run := b.loginRun
	if run == nil {
		run = b.runLogin
	}
	// Deferred, so a panic in run cannot keep the slot forever.
	stored := false
	defer func() {
		if !stored {
			b.logins.release(m.Chat.ID)
		}
	}()
	stored = run(ctx, m, thread, rc)
}

// loginFailed logs a failed login start and tells the user without the
// error text (paths, container ids and stderr stay in the log).
func (b *Bot) loginFailed(ctx context.Context, chatID int64, thread int, folder, what string, err error) {
	b.log.Error("/login: "+what+" failed",
		slog.String("folder", folder),
		slog.String("error_class", "login_failed"))
	if chatID > 0 {
		b.replyLoginText(ctx, chatID, thread, loginTextFailed)
	}
}

// runLogin starts the agent's login exec and stores the session.
// Reports whether a session was stored (it then owns the chat's slot).
func (b *Bot) runLogin(ctx context.Context, m *models.Message, thread int, rc store.RegisteredChat) bool {
	return b.runLoginTarget(ctx, m, thread, rc, false)
}
func (b *Bot) runLoginTarget(ctx context.Context, m *models.Message, thread int, rc store.RegisteredChat, confirmedGroup bool) bool {
	if !privateLoginMessage(m) || (!confirmedGroup && rc.JID != buildChatJID(m.Chat.ID)) || (confirmedGroup && (m.Chat.ID != b.ownerChatID || userID(m.From) != b.ownerChatID || rc.IsOwner || rc.Folder == "owner")) {
		if m != nil {
			b.cancelUnsafeLogin(m.Chat.ID)
		}
		return false
	}
	if b.runner == nil {
		b.replyLoginText(ctx, m.Chat.ID, thread,
			"Runner is not available. Is Docker running?")
		return false
	}

	tier := runner.TierRegistered
	if rc.IsOwner && !confirmedGroup {
		tier = runner.TierOwner
	}

	// Ensure container is running.
	containerID, err := b.runner.Ensure(ctx, rc.Folder, tier, b.allowlist)
	if err != nil && confirmedGroup && errors.Is(err, runner.ErrProfileUnavailable) {
		b.groupLoginGuidance(ctx, m.Chat.ID, thread, rc)
		return false
	}
	if err != nil {
		b.loginFailed(ctx, m.Chat.ID, thread, rc.Folder, "ensure container", err)
		return false
	}

	b.replyLoginText(ctx, m.Chat.ID, thread, "Starting authentication flow...")

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
		b.loginFailed(ctx, m.Chat.ID, thread, rc.Folder, "ExecInteractive", err)
		return false
	}

	// Closing the attachment bounds a blocked ReadLine when the flow times out.
	context.AfterFunc(loginCtx, func() { _ = exec.Close() })

	// Read lines looking for URL and optional device code.
	var url, deviceCode string
	for i := 0; i < 30; i++ {
		line, err := exec.ReadLine()
		if err != nil {
			if err == io.EOF {
				break
			}
			b.log.Error("/login: output unavailable", slog.String("error_class", "read_failed"))
			break
		}
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
		_ = exec.Close()
		b.replyLoginText(ctx, m.Chat.ID, thread,
			"Could not extract the login URL. Check gopod logs.")
		return false
	}

	// Store the session.
	session := &loginSession{
		exec:       exec,
		ctx:        loginCtx,
		cancel:     loginCancel,
		chatID:     m.Chat.ID,
		folder:     rc.Folder,
		thread:     thread,
		awaitsCode: deviceCode == "",
		private:    true,
		actorID:    userID(m.From),
		tier:       tier,
		provider:   prov.Name(),
		loginURL:   url,
		deviceCode: deviceCode,
	}
	if session.actorID == 0 {
		session.actorID = m.Chat.ID
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
	if _, err := b.sendPrivateLoginText(ctx, m.Chat.ID, thread, loginMsg, nil); err != nil {
		b.cleanupLoginSession(m.Chat.ID, session)
		return false
	}

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
		if b.logins.removeIf(m.Chat.ID, session) {
			_ = exec.Close()
			// Only notify if it was a timeout, not a clean completion.
			if loginCtx.Err() == context.DeadlineExceeded && session.private && session.chatID > 0 {
				b.replyLoginText(context.Background(), m.Chat.ID, thread,
					"Login session timed out. Run /login to try again.")
			}
		}
	}()
	return true
}

// handleLoginCode is called from defaultHandler when the chat has an
// active login session. It writes the user's message (the OAuth code)
// to claude's stdin and reads the result.
func (b *Bot) handleLoginCode(ctx context.Context, chatID int64, code string) {
	session := b.logins.get(chatID)
	if session == nil {
		return
	}
	if chatID <= 0 || !session.private {
		b.cleanupLoginSession(chatID, session)
		return
	}
	b.replyLoginText(ctx, chatID, session.thread, "Verifying code...")
	if session.exec == nil {
		b.cleanupLoginSession(chatID, session)
		return
	}
	if _, err := session.exec.WriteStdin([]byte(code + "\n")); err != nil {
		b.log.Warn("/login: code write failed", slog.String("error_class", "write_failed"))
		b.replyLoginText(ctx, chatID, session.thread, loginTextFailed)
		b.cleanupLoginSession(chatID, session)
		return
	}
	b.finishLogin(ctx, chatID, session)
}

// Completion output is never forwarded or logged: only an auth-status check
// can establish success, even if CLI output says "Not logged in" or "success".
func (b *Bot) finishLogin(ctx context.Context, chatID int64, session *loginSession) {
	if session == nil {
		return
	}
	if chatID <= 0 || !session.private || session.chatID != chatID {
		b.cleanupLoginSession(chatID, session)
		return
	}
	if session.exec != nil {
		for i := 0; i < 30; i++ {
			line, err := session.exec.ReadLine()
			if err != nil {
				break
			}
			lower := strings.ToLower(stripANSI(line))
			if strings.Contains(lower, "logged in") || strings.Contains(lower, "authenticated") || strings.Contains(lower, "success") || strings.Contains(lower, "error") || strings.Contains(lower, "failed") || strings.Contains(lower, "invalid") {
				break
			}
		}
	}
	if session.ctx != nil && session.ctx.Err() == context.DeadlineExceeded {
		return
	}
	b.cleanupLoginSession(chatID, session)
	verified := false
	if b.loginAuthCheck != nil {
		verified = b.loginAuthCheck(ctx, session)
	} else {
		verified = b.checkAuthAfterLogin(ctx, session)
	}
	if verified {
		b.replyLoginText(ctx, chatID, session.thread, "Authentication verified. Send a message to start chatting.")
	} else {
		b.replyLoginText(ctx, chatID, session.thread, "Authentication could not be verified. Run /login in this private chat to try again.")
	}
}

// checkAuthAfterLogin does a quick `claude auth status --json` poll
// to verify the login succeeded even if stdout was ambiguous.
func (b *Bot) checkAuthAfterLogin(ctx context.Context, session *loginSession) bool {
	if b.runner == nil {
		return false
	}
	tier := runner.TierRegistered
	if session.provider != "" && b.runner.ProviderForChat(session.folder).Name() != session.provider {
		return false
	}
	tier = session.tier
	status, err := b.runner.CheckAuth(ctx, session.folder, tier, b.allowlist)
	if err != nil {
		return false
	}
	return status.LoggedIn
}

func (b *Bot) cleanupLoginSession(chatID int64, session *loginSession) {
	if session == nil {
		return
	}
	b.logins.removeIf(chatID, session)
	if session.cancel != nil {
		session.cancel()
	}
	if session.exec != nil {
		_ = session.exec.Close()
	}
}
func (ls *loginSessions) removeIf(chatID int64, session *loginSession) bool {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if ls.sessions[chatID] != session {
		return false
	}
	delete(ls.sessions, chatID)
	return true
}
func (b *Bot) cancelUnsafeLogin(chatID int64) {
	if b.logins == nil {
		return
	}
	if session := b.logins.get(chatID); session != nil {
		b.cleanupLoginSession(chatID, session)
	}
	b.logins.release(chatID)
}

// Consume raw authorization codes before persistence, media processing or
// model routing. Commands stay commands, but their quoted login URL is not saved.
func (b *Bot) interceptLoginInput(ctx context.Context, m *models.Message) (consumed, sensitive bool) {
	if m == nil || b.logins == nil {
		return false, false
	}
	session := b.logins.get(m.Chat.ID)
	if session == nil {
		return false, false
	}
	raw := strings.TrimSpace(messageText(m))
	if !privateLoginMessage(m) || !session.private {
		b.cancelUnsafeLogin(m.Chat.ID)
		return !strings.HasPrefix(raw, "/"), true
	}
	if !session.awaitsCode {
		if raw != "" && (session.deviceCode != "" && strings.Contains(raw, session.deviceCode) || session.loginURL != "" && strings.Contains(raw, session.loginURL)) {
			b.replyLoginText(ctx, m.Chat.ID, session.thread, "Complete sign-in in your browser. Do not send the device code to this chat.")
			return true, true
		}
		return false, true
	}
	if session.actorID != 0 && session.actorID != userID(m.From) {
		return !strings.HasPrefix(raw, "/"), true
	}
	if raw == "" || strings.HasPrefix(raw, "/") {
		return false, true
	}
	if b.loginCode != nil {
		b.loginCode(ctx, m.Chat.ID, raw)
	} else {
		b.handleLoginCode(ctx, m.Chat.ID, raw)
	}
	return true, true
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

func isLoginCommand(raw string) bool {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return false
	}
	command, _, _ := strings.Cut(fields[0], "@")
	return command == "/login"
}
