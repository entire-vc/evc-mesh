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
	{CodeDocumentSlugTaken, "A document with this slug already exists in this folder. Rename one of them or choose another slug.", "Документ с таким slug уже есть в этой папке. Переименуйте один из них или выберите другой slug."},
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
	{"task_already_checked_out", "This task is checked out by someone else. Try again after the checkout expires.", "Задачу уже взял в работу другой исполнитель. Повторите после окончания его блокировки."},
	{"key_rejected", "Team Relay rejected the access key. Check the key in the project's Team Relay settings.", "Team Relay отклонил ключ доступа. Проверьте ключ в настройках Team Relay проекта."},
	{"foreign_share", "This share belongs to a different Team Relay account, so it cannot be mounted.", "Этот общий доступ принадлежит другой учётной записи Team Relay, подключить его нельзя."},
	{"key_expired", "The Team Relay access key has expired. Issue a new key and try again.", "Срок действия ключа доступа Team Relay истёк. Выпустите новый ключ и повторите."},
	{"unreachable", "Team Relay is unreachable. Try again later.", "Team Relay недоступен. Повторите позже."},
	{"share_not_found", "The Team Relay share was not found. Check that it still exists.", "Общий доступ Team Relay не найден. Проверьте, что он ещё существует."},
	{"error", "Team Relay sync failed. Try again later.", "Синхронизация с Team Relay не удалась. Повторите позже."},
	{"extension_builtin_runtime", "Builtin extensions ship with Mesh and cannot be installed from a repository.", "Встроенные расширения поставляются с Mesh и не ставятся из репозитория."},
	{"extension_incompatible", "This extension is incompatible with this version of Mesh.", "Расширение несовместимо с этой версией Mesh."},
	{"extension_manifest_invalid", "The repository's mesh-extension.json is not a valid manifest.", "Файл mesh-extension.json в репозитории не является корректным манифестом."},
	{"extension_id_conflict", "An extension with this id is already installed.", "Расширение с таким id уже установлено."},
	{"extension_repo_unavailable", "The repository can't be read: check the address, the ref and the token.", "Не удаётся прочитать репозиторий: проверьте адрес, тег или ветку и токен."},
	{"extension_not_allowed", "This extension is not allowed on this instance. An instance administrator has to allow it first.", "Это расширение не разрешено на этом сервере. Сначала его должен разрешить администратор."},
	{"chat_protocol_unsupported", "This chat protocol version is not supported.", "Эта версия протокола чата не поддерживается."},
	{"chat_not_claim_holder", "This turn is held by another runner.", "Ход выполняет другой раннер."},
	{"chat_turn_not_found", "The chat turn was not found.", "Ход чата не найден."},
	{"chat_stale_claim", "The turn was handed to another claim. Stop working on it.", "Ход передан по новому захвату. Прекратите работу над ним."},
	{"chat_lease_expired", "The turn lease has expired. Stop working on it.", "Аренда хода истекла. Прекратите работу над ним."},
	{"chat_turn_terminal", "The turn is already finished.", "Ход уже завершён."},
	{"chat_not_started", "The turn has not been started yet.", "Ход ещё не запущен."},
	{"chat_seq_gap", "An event is missing: resend from the expected sequence number.", "Пропущено событие: повторите отправку с ожидаемого номера."},
	{"chat_batch_too_large", "The event batch is too large.", "Пачка событий слишком большая."},
	{"chat_rate_limited", "Too many event batches for this turn. Try again shortly.", "Слишком много пачек событий для этого хода. Повторите чуть позже."},
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
