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
