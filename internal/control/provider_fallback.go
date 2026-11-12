package control

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Coriol-is/gopod/internal/store"
)

// ProviderFallbackSwitchFunc compares the expected provider under the runner's
// agent/lifecycle locks, so old buttons cannot overwrite a newer manual choice.
type ProviderFallbackSwitchFunc func(context.Context, string, string, string) error

var ErrFallbackStale = errors.New("provider fallback is stale")

func FallbackSourceID(turnID int64) string { return "fallback:" + strconv.FormatInt(turnID, 10) }
func QuotaOfferKey(turnID int64) string    { return "quota_offer:" + strconv.FormatInt(turnID, 10) }

// RegisterProviderFallbackCommand has no slash alias. Only a single-use
// confirmation minted after a quota error can run it; text arguments never
// manufacture Command.Confirmed. Every dispatch rechecks chat permissions.
func RegisterProviderFallbackCommand(r *Router, st *store.Store, switchFn ProviderFallbackSwitchFunc, retryFn func(context.Context, store.Turn) (bool, error)) {
	registerProviderFallback(r, st, switchFn, nil, retryFn)
}

type ConversationFallbackSwitchFunc func(context.Context, store.ConversationSelection, string) error

func RegisterConversationFallbackCommand(r *Router, st *store.Store, switchFn ConversationFallbackSwitchFunc, retryFn func(context.Context, store.Turn) (bool, error)) {
	registerProviderFallback(r, st, nil, switchFn, retryFn)
}
func registerProviderFallback(r *Router, st *store.Store, switchFn ProviderFallbackSwitchFunc, scoped ConversationFallbackSwitchFunc, retryFn func(context.Context, store.Turn) (bool, error)) {
	r.Register("provider.fallback", "", "confirm a quota fallback and retry once", PermChatLocal, func(ctx context.Context, cmd Command) (Response, error) {
		refuse := func(text string) (Response, error) { return Response{Text: text, Code: 1}, nil }
		if groupMemberDenied(cmd.Caller) {
			return refuse("Only the group owner can change the provider.")
		}
		wantArgs := 3
		if scoped != nil {
			wantArgs = 5
		}
		if !cmd.Confirmed || len(cmd.Args) != wantArgs || cmd.Caller.Source != SourceTelegram {
			return refuse("Подтвердите переключение кнопкой под сообщением о лимите.")
		}
		id, err := strconv.ParseInt(cmd.Args[0], 10, 64)
		if err != nil || id <= 0 {
			return refuse("Предложение устарело.")
		}
		expected, target := cmd.Args[1], cmd.Args[2]
		if !((expected == "claude" && target == "codex") || (expected == "codex" && target == "claude")) {
			return refuse("Предложение устарело.")
		}
		turn, err := st.GetTurn(ctx, id)
		if err != nil {
			return refuse("Запрос не найден.")
		}
		rc, err := st.GetRegistered(ctx, fmt.Sprintf("tg:%d", cmd.Caller.ChatID))
		if err != nil || turn.ChatID != cmd.Caller.ChatID || turn.ChatFolder != rc.Folder || turn.ThreadID != cmd.Caller.ThreadID || turn.Source != "telegram" || strings.HasPrefix(turn.SourceID, "fallback:") || turn.ReplyMsgID != 0 {
			return refuse("Это предложение не относится к текущему диалогу.")
		}
		offered, err := st.GetState(ctx, QuotaOfferKey(id))
		if err != nil || offered != expected {
			return refuse("Предложение устарело.")
		}
		if _, exists, err := st.TurnBySource(ctx, "telegram", FallbackSourceID(id)); err != nil {
			return refuse("Не удалось проверить повтор запроса. Попробуйте позже.")
		} else if exists {
			return Response{Text: "Запрос уже поставлен в очередь."}, nil
		}
		// A confirmed group retry is a new action by the owner, never the
		// sender identity stored on the original turn.
		if turn.ChatID < 0 {
			mode, err := st.GroupTrigger(ctx, rc.JID)
			if err != nil {
				return refuse("Group policy unavailable.")
			}
			text := turn.Text
			if strings.HasPrefix(text, "[gopod group: ") {
				if _, rest, ok := strings.Cut(text, "\n\n"); ok {
					text = rest
				}
			}
			turn.Text = "[gopod group: trigger=" + string(mode) + "; sender=owner]\n\n" + text
		}
		var switchErr error
		if scoped != nil {
			attempt, e := strconv.Atoi(cmd.Args[3])
			revision, e2 := strconv.ParseInt(cmd.Args[4], 10, 64)
			if e != nil || e2 != nil || attempt != turn.Attempts {
				return refuse("Предложение устарело.")
			}
			snapshot, e := st.TurnSelection(ctx, turn.ID, attempt)
			if e != nil || snapshot.Provider != expected || snapshot.Revision != revision {
				return refuse("Предложение устарело.")
			}
			switchErr = scoped(ctx, snapshot, target)
		} else {
			switchErr = switchFn(ctx, rc.Folder, expected, target)
		}
		if err := switchErr; err != nil {
			if errors.Is(err, ErrFallbackStale) {
				return refuse("Провайдер уже изменён. Повторите запрос в выбранном провайдере.")
			}
			return refuse("Не удалось переключить провайдера. Повторите попытку позже.")
		}
		// Use current registration's owner flag; input data cannot elevate permissions.
		turn.IsOwner = rc.IsOwner
		_, err = retryFn(ctx, turn)
		if err != nil {
			return refuse("Провайдер переключён на " + target + ", но запрос не поставлен в очередь. Отправьте исходный запрос заново.")
		}
		scope := "для всего чата"
		if scoped != nil {
			scope = "для этого диалога"
		}
		return Response{Text: "Переключено на " + target + " " + scope + ". Контекст сохранён; запрос поставлен в очередь в этой теме."}, nil
	})
}
