package i18n

var catalogRU = map[string]string{
	"notif.task.assigned.title":          "Назначена задача: {task}",
	"notif.task.reviewer_assigned.title": "Запрошено ревью: {task}",
	"notif.task.ready_for_review.title":  "Готово к ревью: {task}",
	"notif.task.status_changed.title":    "Статус задачи изменён: {task}",
	"notif.comment.created.title":        "Новый комментарий к: {task}",

	"notif.task.mentioned.title":      "{actor} упомянул(а) вас в: {task}",
	"notif.task.mentioned_anon.title": "Вас упомянули в: {task}",

	"notif.task.blocking_triage.title": "Блокирующий вопрос: {task}",
	"notif.task.blocking_triage.body":  "@{user} задал(а) блокирующий вопрос по этой задаче.",

	"notif.extension.blocked.title": "Расширение выключено: {extension}",
	"notif.extension.blocked.body":  "Блок-лист Spark выключил «{extension}» на этом инстансе. Причина: {reason}",

	"notif.task.human_gate_default_applied.title": "Применено значение по умолчанию: {task}",
	"notif.task.human_gate_default_applied.body":  "Срок гейта истёк без ответа — применено рекомендованное значение: {default}",

	"notif.doc.mentioned.title":      "{actor} упомянул(а) вас в: {doc}",
	"notif.doc.mentioned_anon.title": "Вас упомянули в: {doc}",
	"notif.doc.commented.title":      "{actor} прокомментировал(а): {doc}",
	"notif.doc.deleted.title":        "{actor} удалил(а): {doc}",
	"notif.doc.deleted.body":         "Документ «{doc}», за которым вы следили, удалён пользователем {actor}.",

	"notif.doc.changed.title":            "{actor} изменил(а): {doc}",
	"notif.doc.changed.body.one":         "{actor}: {count} правка.",
	"notif.doc.changed.body.few":         "{actor}: {count} правки.",
	"notif.doc.changed.body.many":        "{actor}: {count} правок.",
	"notif.doc.renamed.title":            "{actor} изменил(а): {doc}",
	"notif.doc.renamed.body":             "{actor} переименовал(а) страницу: «{doc}».",
	"notif.doc.renamed_edited.title":     "{actor} изменил(а): {doc}",
	"notif.doc.renamed_edited.body.one":  "{actor} переименовал(а) страницу и внёс(ла) {count} правку.",
	"notif.doc.renamed_edited.body.few":  "{actor} переименовал(а) страницу и внёс(ла) {count} правки.",
	"notif.doc.renamed_edited.body.many": "{actor} переименовал(а) страницу и внёс(ла) {count} правок.",

	"notif.actor.someone": "Кто-то",

	"notif.email.view_task": "Открыть задачу",
	"notif.email.footer":    "Вы получаете это письмо из-за настроек уведомлений Mesh. Изменить их можно через колокольчик в приложении.",
	"invite.email.subject":  "Вас пригласили в {workspace} в Mesh",
	"invite.email.heading":  "Вас пригласили в {workspace}",
	"invite.email.body":     "Нажмите кнопку ниже, чтобы принять приглашение и создать аккаунт.",
	"invite.email.button":   "Принять приглашение",
	"invite.email.expiry":   "Ссылка действует 7 дней. Если вы не ждали приглашения, просто проигнорируйте письмо.",

	"bot.start.unlinked":        "Бот доступен только пользователям Mesh — получите персональную ссылку в настройках уведомлений вашего воркспейса.",
	"bot.welcome.name_fallback": "коллега",
	"bot.welcome.no_projects":   "пока нет проектов",
	"bot.welcome":               "Добро пожаловать, {name}, ваша учётная запись успешно привязана.\nВот список ваших проектов:\n{projects}",
}
