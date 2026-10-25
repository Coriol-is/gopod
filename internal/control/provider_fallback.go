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
	r.Register("provider.fallback", "", "confirm a quota fallback and retry once", PermChatLocal, func(ctx context.Context, cmd Command) (Response, error) {
		refuse := func(text string) (Response, error) { return Response{Text: text, Code: 1}, nil }
		if !cmd.Confirmed || len(cmd.Args) != 3 || cmd.Caller.Source != SourceTelegram {
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
		if err := switchFn(ctx, rc.Folder, expected, target); err != nil {
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
		return Response{Text: "Переключено на " + target + " для всего чата. Контекст сохранён; запрос поставлен в очередь в этой теме."}, nil
	})
}
