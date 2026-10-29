package telegram

// User-facing strings of the inline-button layer, in Russian (spec Q4:
// Russian for everything the bot itself says). New strings go here;
// Phase 5 of the dynamic UX plan moves the older ones in as well.

// Callback toasts (answerCallbackQuery text, at most 200 characters).
const (
	cbToastExpired       = "Кнопка устарела"
	cbToastNotRegistered = "Этот чат не подключён"
	cbToastFailed        = "Не получилось, попробуйте ещё раз"
)

// Stop button (keyboard.go).
const (
	btnStop           = "⏹ Стоп"
	stopToastStopping = "Останавливаю…"
	stopToastFinished = "Уже завершено"
)

// Retry and login buttons (actions.go, errors.go).
const (
	btnRetry = "🔁 Повторить"
	btnLogin = "🔑 Войти"

	cbToastNotYours    = "Эта кнопка не для этого чата"
	retryToastQueued   = "Повторяю запрос"
	retryToastAlready  = "Уже повторяю"
	loginToastStarting = "Запускаю вход…"
	loginToastAlready  = "Вход уже запущен"
	loginToastInStart  = "Вход запускается, подождите…"
	retryToastFailed   = "Не получилось повторить, попробуйте ещё раз"
)

// Login flow (login.go).
const (
	loginTextAlready = "Вход уже запущен в этом чате. Вставьте код из браузера или дождитесь окончания сессии."
	loginTextFailed  = "Не удалось запустить вход. Попробуйте /login ещё раз чуть позже."
	// The login is still starting (container, CLI): no URL yet.
	loginTextStarting = "Вход ещё запускается, подождите ссылку…"
)

// Status header (statusline.go): elapsed time and steps of a working
// turn, or its queue position.
const (
	headerQueued = "в очереди: %d-й"
	headerSteps1 = "шаг"
	headerSteps2 = "шага"
	headerSteps5 = "шагов"
)

// Error replies (errors.go): what happened and what to do next. Never
// the error text itself: no stderr, paths or tokens reach the chat.
const (
	errTextAuthExpired = "Сессия агента истекла. Нажмите «Войти» или отправьте /login."
	errTextNotLoggedIn = "Агент не авторизован. Нажмите «Войти» или отправьте /login."
	errTextUsageLimit  = "Достигнут лимит использования AI-провайдера. Дождитесь сброса лимита и повторите запрос."
	errTextAgentFailed = "Агент не смог ответить: запуск завершился ошибкой. Можно повторить запрос."
	errTextBusy        = "Агент сейчас занят. Повторите запрос чуть позже."
	errTextDelivery    = "Не удалось доставить ответ. Можно повторить запрос."
	// Shown in groups when the agent runtime refuses a turn; it names no cause.
	errTextGroupUnavailable = "Агент в этом чате сейчас недоступен."
)

// Reply action row under the newest reply (keyboard.go, replyactions.go).
const (
	btnRegenerate = "🔁 Заново"
	btnCopy       = "📋 Копировать"
	btnMore       = "⋯"
	btnSteps      = "🧭 Шаги"
	btnAsFile     = "📄 Файлом"
	btnBack       = "‹ Назад"

	regenToastQueued  = "Готовлю другой ответ"
	regenToastAlready = "Уже готовлю другой ответ"
	regenToastFailed  = "Не получилось запросить другой ответ, попробуйте ещё раз"
	stepsToastNone    = "Шаги этого ответа не сохранились"
	fileToastGone     = "Текст этого ответа больше недоступен"
	fileToastFailed   = "Не получилось отправить файл, попробуйте ещё раз"
	stepsHeader       = "Шаги агента:"
)

// Command buttons, confirmations and pages (cmdbuttons.go, pages.go).
const (
	cmdTextDone      = "Готово."
	cmdTextCancelled = "Отменено."

	btnPagePrev       = "‹"
	btnPageNext       = "›"
	pagesToastExpired = "Страницы устарели, повторите команду"
)

// Quota fallback is offered, never executed without a single-use confirmation.
const (
	quotaSwitchQuestion = "Достигнут лимит %s. Переключить весь чат на %s и один раз повторить этот запрос в текущей теме? Контекст сохранится. Для второго провайдера может понадобиться /login."
	btnSwitchContinue   = "Переключить и продолжить"
	btnFallbackCancel   = "Отмена"
)
