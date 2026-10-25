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
	retryToastFailed   = "Не получилось повторить, попробуйте ещё раз"
)

// Login flow (login.go).
const (
	loginTextAlready = "Вход уже запущен в этом чате. Вставьте код из браузера или дождитесь окончания сессии."
	loginTextFailed  = "Не удалось запустить вход. Попробуйте /login ещё раз чуть позже."
)

// Error replies (errors.go): what happened and what to do next. Never
// the error text itself: no stderr, paths or tokens reach the chat.
const (
	errTextAuthExpired = "Сессия агента истекла. Нажмите «Войти» или отправьте /login."
	errTextNotLoggedIn = "Агент не авторизован. Нажмите «Войти» или отправьте /login."
	errTextAgentFailed = "Агент не смог ответить: запуск завершился ошибкой. Можно повторить запрос."
	errTextBusy        = "Агент сейчас занят. Повторите запрос чуть позже."
	errTextDelivery    = "Не удалось доставить ответ. Можно повторить запрос."
)
