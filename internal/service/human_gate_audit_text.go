package service

import (
	"fmt"

	"github.com/google/uuid"
)

// Audit-trail comment bodies the handlers write when a human_gate is armed or
// cleared outside the comment path. They live next to rawArmMarkerSubstring
// because the scans that read a thread match on this exact wording, so the
// text and the matcher must change together; the handler package carries no
// natural-language strings of its own.

// GateRawArmAuditBody is the system comment left by a raw PATCH/UI arm. It
// must contain rawArmMarkerSubstring (see hasRawArmMarker).
func GateRawArmAuditBody(actorID uuid.UUID, actorType string) string {
	return fmt.Sprintf("🔒 Auto: %s, без маркерного коммента — actor: %s (%s)",
		rawArmMarkerSubstring+" (PATCH/UI)", actorID, actorType)
}

// GateClearedByOwnAPIArmAuditBody is the comment left when the author of an
// API-armed gate clears it through DELETE /human-gate.
func GateClearedByOwnAPIArmAuditBody(actorID uuid.UUID) string {
	return fmt.Sprintf("🔓 Auto: human_gate снят автором собственного API-арма (agent: %s). "+
		"Маркерного коммента у этого гейта не было — снятие через DELETE /human-gate, "+
		"не отзыв аска.", actorID)
}

// GateClearedManuallyAuditBody is the comment left when a user clears the gate
// through PATCH/UI.
func GateClearedManuallyAuditBody(actorID uuid.UUID) string {
	return fmt.Sprintf("🔓 human_gate снят вручную (actor: %s)", actorID)
}

// WithdrawalNegatorExamples is the quoted sample of negator words handed back
// in a refusal hint; the full vocabulary is triageExitNegators.
func WithdrawalNegatorExamples() string {
	return `"не нужен", "не нужно", "не требуется", "снят", "resolved"`
}
