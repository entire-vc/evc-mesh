package service

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// These mirror bob/scripts/test_secret_rotation_gate_e8388213.py's TestClassify
// case-for-case (task #bb1aaa09, tail of #e8388213) — the Go port must classify the
// SAME population the same way, not merely "a reasonable subset". The two named traps
// (an alert about the leak-COUNTER itself, and a `logrotate` alert) are the ones that
// broke the original naive title-regex proposal on a live 07.09 board snapshot.

func secretRotationCard(title string, labels ...string) *domain.Task {
	return &domain.Task{Title: title, Labels: labels}
}

func TestSecretRotationClassify_ExplicitLabelIsAuthoritative(t *testing.T) {
	for _, lbl := range []string{"secret-leak", "secret-rotation", "rotation", "secret-hygiene"} {
		t.Run(lbl, func(t *testing.T) {
			tier, _ := secretRotationClassify(secretRotationCard("что угодно", lbl))
			assert.Equal(t, "label", tier)
		})
	}
}

func TestSecretRotationClassify_AlertAboutTheCounterItselfIsNeverParked(t *testing.T) {
	// THE dangerous false positive: this is the alarm that the leak counter stopped
	// running. Parking it silences the instrument, the silence reads as "no leaks",
	// and the systemic rotation would eventually fire on a dead measurement.
	task := secretRotationCard(
		"[ALERT] CalendarWindowMissed — vc.entire.mcp-secret-leak-watch",
		"alert", "auto-incident", "severity:critical", "no-pavel-triage",
	)
	tier, _ := secretRotationClassify(task)
	assert.Equal(t, "", tier)
}

func TestSecretRotationClassify_LogrotateAlertIsNotASecretRotation(t *testing.T) {
	task := secretRotationCard(
		"[ALERT] CalendarWindowMissed — vc.entire.mcp-wrap-logrotate",
		"alert", "auto-incident", "severity:critical",
	)
	tier, _ := secretRotationClassify(task)
	assert.Equal(t, "", tier)
}

func TestSecretRotationClassify_ExclusionBeatsAPositiveLabel(t *testing.T) {
	// Order matters: an excluded card stays excluded even carrying a positive label
	// too, or the counter-alert above could park itself.
	task := secretRotationCard("[ALERT] что-то про ротацию ключа", "alert", "rotation")
	tier, _ := secretRotationClassify(task)
	assert.Equal(t, "", tier)
}

func TestSecretRotationClassify_OptoutLabelWinsOverEverything(t *testing.T) {
	task := secretRotationCard("Ротация токена", "rotation", secretRotationNotSecretRotationLabel)
	tier, _ := secretRotationClassify(task)
	assert.Equal(t, "", tier)
}

func TestSecretRotationClassify_ActiveExploitationKeepsItsGate(t *testing.T) {
	task := secretRotationCard("Ключ утёк и им пользуются снаружи — ротация",
		"security", "incident", "severity:critical")
	tier, _ := secretRotationClassify(task)
	assert.Equal(t, "", tier)
}

func TestSecretRotationClassify_PlainIncidentLabelDoesNotExcuseACard(t *testing.T) {
	// `incident` describes how the leak happened and sits on most of the class;
	// excluding on it would void the whole mechanism. Only `severity:critical` carries
	// the exploitation claim.
	task := secretRotationCard("Утечка: bare export вывалил окружение — ротировать 3 токена",
		"security", "incident", "credentials")
	tier, _ := secretRotationClassify(task)
	assert.Equal(t, "title", tier)
}

func TestSecretRotationClassify_AuditPlanCardDoesNotParkItself(t *testing.T) {
	task := secretRotationCard("1.18 Метка secret-leak/rotation: карточка сразу в backlog",
		"audit-2026-09", "phase-1", "plan:1.18")
	tier, _ := secretRotationClassify(task)
	assert.Equal(t, "", tier)
}

func TestSecretRotationClassify_HumanVerifyChoreIsLeftToItsOwnLane(t *testing.T) {
	task := secretRotationCard("AC4 human-verify: ротация токена интеграции через UI",
		"mesh", "kind:human-verify")
	tier, _ := secretRotationClassify(task)
	assert.Equal(t, "", tier)
}

func TestSecretRotationClassify_UnderscoreCredentialNamesAreNouns(t *testing.T) {
	// `\btoken\b` does NOT match inside `CLAUDE_CODE_OAUTH_TOKEN` — `_` is a word
	// character, so there is no boundary. This gap hid two live rotation cards.
	titles := []string{
		"[fiddler] Отозвать утёкший CLAUDE_CODE_OAUTH_TOKEN (живой, в git-истории)",
		"Riker's live Mesh agent_key sits in 63 local transcripts — rotate",
		"Live GH_TOKEN/GITLAB_TOKEN for lanes printed via pgrep dump",
	}
	for _, title := range titles {
		t.Run(title, func(t *testing.T) {
			tier, _ := secretRotationClassify(secretRotationCard(title, "security"))
			assert.Equal(t, "title", tier)
		})
	}
}

func TestSecretRotationClassify_RussianCredentialNouns(t *testing.T) {
	titles := []string{
		"Ротация 4 учётных данных, лежащих в прод-памяти открытым текстом",
		"MinIO offsite creds printed in agent session output — rotate",
		"Ключ Bing Webmaster напечатан в выводе инструмента — нужна ротация",
	}
	for _, title := range titles {
		t.Run(title, func(t *testing.T) {
			tier, _ := secretRotationClassify(secretRotationCard(title, "security"))
			assert.Equal(t, "title", tier)
		})
	}
}

func TestSecretRotationClassify_VerbWithoutACredentialNounIsNotEnough(t *testing.T) {
	task := secretRotationCard("Editing the 14 leaked comments did not retract them", "security")
	tier, _ := secretRotationClassify(task)
	assert.Equal(t, "", tier)
}

func TestSecretRotationClassify_NounWithoutAVerbIsNotEnough(t *testing.T) {
	task := secretRotationCard("Добавить поле token в форму создания задачи", "security")
	tier, _ := secretRotationClassify(task)
	assert.Equal(t, "", tier)
}

func TestSecretRotationClassify_JobNamesAreStrippedBeforeMatching(t *testing.T) {
	task := secretRotationCard("24h-замер: доля значений в secret-leak-watch.jsonl", "security")
	tier, _ := secretRotationClassify(task)
	assert.Equal(t, "", tier)
}

func TestSecretRotationSuppressionReason_EmptyForOrdinaryCard(t *testing.T) {
	task := secretRotationCard("Обычная задача про redesign лендинга")
	assert.Empty(t, secretRotationSuppressionReason(task))
}

func TestSecretRotationSuppressionReason_NamesTheUndoLabelPath(t *testing.T) {
	task := secretRotationCard("что угодно", "secret-leak")
	reason := secretRotationSuppressionReason(task)
	assert.NotEmpty(t, reason)
	assert.Contains(t, reason, "label(s) secret-leak")
	assert.Contains(t, reason, "e8388213")
}
