package i18n

// Notification codes: <code>.title is the subject line, <code>.body the body.
// A code without a .body entry keeps the event's own body (user-authored text
// such as a comment, which is not translated).
var catalogEN = map[string]string{
	"notif.task.assigned.title":          "Task assigned: {task}",
	"notif.task.reviewer_assigned.title": "Review requested: {task}",
	"notif.task.ready_for_review.title":  "Ready for review: {task}",
	"notif.task.status_changed.title":    "Task status changed: {task}",
	"notif.comment.created.title":        "New comment on: {task}",

	"notif.task.mentioned.title":      "{actor} mentioned you on: {task}",
	"notif.task.mentioned_anon.title": "You were mentioned on: {task}",

	"notif.task.blocking_triage.title": "Blocking question: {task}",
	"notif.task.blocking_triage.body":  "@{user} asked a blocking question on this task.",

	"notif.task.human_gate_default_applied.title": "Default applied: {task}",
	"notif.task.human_gate_default_applied.body":  "Gate deadline passed with no answer — recommended default applied: {default}",

	"notif.doc.mentioned.title":      "{actor} mentioned you on: {doc}",
	"notif.doc.mentioned_anon.title": "You were mentioned on: {doc}",
	"notif.doc.commented.title":      "{actor} commented on: {doc}",
	"notif.doc.deleted.title":        "{actor} deleted: {doc}",
	"notif.doc.deleted.body":         "The document «{doc}» you were watching was deleted by {actor}.",

	"notif.doc.changed.title":             "{actor} edited: {doc}",
	"notif.doc.changed.body.one":          "{actor} made {count} edit.",
	"notif.doc.changed.body.other":        "{actor} made {count} edits.",
	"notif.doc.renamed.title":             "{actor} edited: {doc}",
	"notif.doc.renamed.body":              "{actor} renamed the page to «{doc}».",
	"notif.doc.renamed_edited.title":      "{actor} edited: {doc}",
	"notif.doc.renamed_edited.body.one":   "{actor} renamed the page and made {count} edit.",
	"notif.doc.renamed_edited.body.other": "{actor} renamed the page and made {count} edits.",

	"notif.actor.someone": "Someone",

	"notif.email.view_task": "View task",
	"notif.email.footer":    "You're receiving this because of your Mesh notification settings. You can change them from the notification bell in the app.",
	"invite.email.subject":  "You've been invited to {workspace} on Mesh",
	"invite.email.heading":  "You've been invited to {workspace}",
	"invite.email.body":     "Click the button below to accept the invitation and set up your account.",
	"invite.email.button":   "Accept Invitation",
	"invite.email.expiry":   "This link expires in 7 days. If you weren't expecting this, you can ignore this email.",

	"bot.start.unlinked":        "This bot is for Mesh users only. Get your personal link in your workspace notification settings.",
	"bot.welcome.name_fallback": "there",
	"bot.welcome.no_projects":   "no projects yet",
	"bot.welcome":               "Welcome, {name}! Your account is linked.\nYour projects:\n{projects}",
}
