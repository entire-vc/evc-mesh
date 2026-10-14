package apierror

// Stable error codes. A code is part of the API contract: never rename or
// reuse one — add a new entry instead. Responses carry the code next to an
// English message; clients translate by code and fall back to the message for
// a code they do not know.
const (
	CodeBadRequest         = "bad_request"
	CodeValidationFailed   = "validation_failed"
	CodeUnauthorized       = "unauthorized"
	CodeForbidden          = "forbidden"
	CodeNotFound           = "not_found"
	CodeConflict           = "conflict"
	CodeRateLimited        = "rate_limited"
	CodeServiceUnavailable = "service_unavailable"
	CodeInternalError      = "internal_error"
)

// Entry is one catalog row: the code and its text in every supported language.
type Entry struct {
	Code string
	EN   string
	RU   string
}

// Catalog is the single list of error codes. The web interface keeps the same
// texts under apiErrors.<code> in src/locales/<lang>/common.json; a test in
// this package fails if the two drift apart.
var Catalog = []Entry{
	{CodeBadRequest, "The request is invalid.", "Некорректный запрос."},
	{CodeValidationFailed, "Validation failed. Check the entered data.", "Проверка не пройдена. Проверьте введённые данные."},
	{CodeUnauthorized, "You need to sign in.", "Требуется вход в систему."},
	{CodeForbidden, "You don't have permission to do this.", "Недостаточно прав для этого действия."},
	{CodeNotFound, "Not found.", "Не найдено."},
	{CodeConflict, "This conflicts with the current state.", "Действие конфликтует с текущим состоянием."},
	{CodeRateLimited, "Too many requests. Try again later.", "Слишком много запросов. Повторите позже."},
	{CodeServiceUnavailable, "The service is temporarily unavailable.", "Сервис временно недоступен."},
	{CodeInternalError, "Internal server error.", "Внутренняя ошибка сервера."},

	{"cas_conflict", "The task was changed by someone else. Reload it and try again.", "Задачу уже изменил кто-то другой. Обновите её и повторите."},
	{"document_version_conflict", "The document was changed since you opened it. Reload it and re-apply your edit.", "Документ изменился с момента открытия. Обновите его и повторите правку."},
	{"external_source_conflict", "The original document changed since you opened it. Nothing was saved. Reopen it and re-apply your edit.", "Оригинал документа изменился с момента открытия. Ничего не сохранено. Откройте его заново и повторите правку."},
	{"external_source_write_landed", "Your edit reached the original document and will appear here at the next sync.", "Правка сохранена в оригинале документа и появится здесь при следующей синхронизации."},
	{"list_revision_stale", "The list changed while you were paging. Start again from the first page.", "Список изменился, пока вы его листали. Начните с первой страницы."},
	{"cursor_expired", "The event cursor has expired. Reload the current state.", "Курсор событий устарел. Загрузите актуальное состояние заново."},
	{"assignee_not_in_workspace", "This assignee is not part of the workspace.", "Этот исполнитель не входит в рабочее пространство."},
	{"assignee_unresolved", "No agent or user matches this assignee.", "Не найден агент или пользователь с таким идентификатором."},
	{"assignee_refuses_assignments", "This agent accepts no assignments. Choose another assignee.", "Этот агент не принимает назначения. Выберите другого исполнителя."},
	{"assignment_pinned", "The assignee was pinned by a person and cannot be changed automatically.", "Исполнителя закрепил человек, автоматически его не сменить."},
	{"mention_handoff_required", "A mention does not wake an agent. Assign the task and move it to todo instead.", "Упоминание не будит агента. Назначьте задачу и переведите её в todo."},
	{"status_change_via_patch_rejected", "Status cannot be changed this way. Use the move action.", "Статус нельзя изменить таким способом. Используйте перемещение задачи."},
	{"review_evidence_required", "Evidence is required to send this task to review.", "Для передачи задачи на проверку нужны подтверждения."},
	{"done_evidence_required", "Evidence is required to close this task.", "Для закрытия задачи нужны подтверждения."},
	{"dod_gate_blocked", "The definition of done is not met yet.", "Критерии готовности ещё не выполнены."},
	{"triage_entry_requires_human_gate", "A task can enter triage only with a question for a person.", "В triage задача попадает только с вопросом к человеку."},
	{"park_requires_wakeup", "A parked task needs a wake-up condition.", "Для отложенной задачи нужно условие пробуждения."},
	{"human_gate_frozen", "This task is waiting for a person's decision and cannot be changed now.", "Задача ждёт решения человека, сейчас её нельзя менять."},
	{"task_shipped", "The task is marked as shipped. Reopen it before moving it.", "Задача помечена как выпущенная. Сначала откройте её заново."},
	{"ambiguous_quote", "The quoted text matches several places. Quote a longer fragment.", "Цитата встречается в нескольких местах. Выделите фрагмент подлиннее."},
	{"export_tree_too_large", "The export is too large.", "Экспорт слишком большой."},
}

// Lookup returns the text of code in locale ("ru" or anything else for
// English), and false when the code is not in the catalog.
func Lookup(code, locale string) (string, bool) {
	for _, e := range Catalog {
		if e.Code == code {
			if locale == "ru" {
				return e.RU, true
			}
			return e.EN, true
		}
	}
	return "", false
}
