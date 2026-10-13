package service

// Codes for server-generated ("system") comments, stored in comments.system_code.
//
// A code names the kind of notice; comments.system_params carries the values
// its text is built from, so a client can render the line in the reader's
// language instead of showing the Russian body. The body stays as the fallback
// for old clients and for comments written before the columns existed (both
// columns are NULL there).
//
// The list is a contract: fiddler (L3.2b) and the writers (L3.2c/d) key on
// these strings, so never rename one — add a new code instead. Each constant
// notes the params the writer is expected to fill.
const (
	// SysCodeMovedToTriage — task moved to triage because of a "Blocking @user"
	// marker above. params: {"target": "<handle>"}.
	SysCodeMovedToTriage = "moved_to_triage"
	// SysCodeSoftGateParked — soft-class gate: task parked in backlog with a
	// due_date instead of triage. params: {"target": "<handle>", "hours": <int>}.
	SysCodeSoftGateParked = "soft_gate_parked"
	// SysCodeSecretRotationSuppressed — gate on Pavel not armed (secret-rotation
	// decision). params: {"reason": "<text>"}.
	SysCodeSecretRotationSuppressed = "secret_rotation_suppressed"
	// SysCodeEndpointGateHint — hint posted under an API/button-armed gate:
	// a comment reply does not lift it. params: none.
	SysCodeEndpointGateHint = "endpoint_gate_hint"
	// SysCodeAPIArmHint — human_gate armed via API/button, lift it with the
	// button / clear_human_gate. params: none.
	SysCodeAPIArmHint = "api_arm_hint"
	// SysCodeGateArmedRaw — gate armed directly (PATCH/UI) without a marker
	// comment. params: {"actor": "<id>", "actor_type": "<type>"}.
	SysCodeGateArmedRaw = "gate_armed_raw"
	// SysCodeGateArmedHard — gate armed, hard class: no auto-apply.
	// params: {"deadline": "<time>"}.
	SysCodeGateArmedHard = "gate_armed_hard"
	// SysCodeGateClearedManual — human_gate cleared by hand. params: {"actor": "<id>"}.
	SysCodeGateClearedManual = "gate_cleared_manual"
	// SysCodeGateClearedOwnArm — gate cleared by the author of its own API arm.
	// params: {"agent": "<id>"}.
	SysCodeGateClearedOwnArm = "gate_cleared_own_arm"
	// SysCodeGateReleasedHumanReply — gate released because the human replied.
	// params: {"from_triage": <bool>}.
	SysCodeGateReleasedHumanReply = "gate_released_human_reply"
	// SysCodeGateReleasedWithdrawn — gate released, the ask's author withdrew it.
	// params: {"from_triage": <bool>}.
	SysCodeGateReleasedWithdrawn = "gate_released_withdrawn"
	// SysCodeGateStillRaised — comment did not lift the gate (withdrawal not
	// recognised). params: {"hint": "<text>"}.
	SysCodeGateStillRaised = "gate_still_raised"
	// SysCodeGateWithdrawalConflict — withdrawal and a new ask in one comment,
	// gate left as is. params: none.
	SysCodeGateWithdrawalConflict = "gate_withdrawal_conflict"
	// SysCodeTriageExit — task moved triage -> in_progress after a human
	// comment. params: none.
	SysCodeTriageExit = "triage_exit"
	// SysCodeReleaseSoftTimeout — soft gate timed out, feed unfrozen, ask stays
	// open. params: {"action": "<text>", "armed_at": "<time>"}.
	SysCodeReleaseSoftTimeout = "release_soft_timeout"
	// SysCodeMonitorUnparked — due_date reached, task returned to todo.
	// params: {"labels": ["<label>"]}.
	SysCodeMonitorUnparked = "monitor_unparked"
	// SysCodeUmbrellaAutoClosed — all descendants terminal, umbrella/epic closed.
	// params: {"children": [{"id": "<short>", "title": "<text>"}]}.
	SysCodeUmbrellaAutoClosed = "umbrella_auto_closed"
	// SysCodeClosedTaskFollowupPending — follow-up on a closed task not yet
	// delivered, will retry. params: {"comment_id": "<id>", "max_attempts": <int>}.
	SysCodeClosedTaskFollowupPending = "closed_task_followup_pending"
	// SysCodeClosedTaskFollowupEscalated — delivery failed after retries.
	// params: {"comment_id": "<id>", "attempts": <int>, "cause": "<text>"}.
	SysCodeClosedTaskFollowupEscalated = "closed_task_followup_escalated"
	// SysCodeClosedTaskFollowupNotice — a closed task does not wake anyone; the
	// remark was moved to a card. params: {"reopened": <bool>, "root": "<short>",
	// "source": "<short>", "comment_id": "<id>"}.
	SysCodeClosedTaskFollowupNotice = "closed_task_followup_notice"
	// SysCodeClosedTaskRepeatDeferred — repeat of a remark not yet delivered
	// (root not visible). params: {"comment_id": "<id>"}.
	SysCodeClosedTaskRepeatDeferred = "closed_task_repeat_deferred"
	// SysCodeRepeatComment — repeat of the same remark on a closed card.
	// params: {"root": "<short>", "reopened": <bool>, "source": "<short>", "comment_id": "<id>"}.
	SysCodeRepeatComment = "repeat_comment"
	// SysCodeAssigneeAutoChanged — automatic assignee change.
	// params: {"from": "<id|null>", "to": "<id|null>", "reason": "<text>"}.
	SysCodeAssigneeAutoChanged = "assignee_auto_changed"
	// SysCodeRecurringRepeat — schedule fired while the instance is still open
	// ("🔁 Повтор N"). params: {"n": <int>}.
	SysCodeRecurringRepeat = "recurring_repeat"
	// SysCodeRecurringMissed — schedule missed consecutive instances.
	// params: {"count": <int>}.
	SysCodeRecurringMissed = "recurring_missed"
	// SysCodeLeaseParked — in_progress without checkout parked.
	// params: {"due_hours": <int>}.
	SysCodeLeaseParked = "lease_parked"
	// SysCodeLeaseBlockerReturned — in_progress without checkout returned to the
	// blocker. params: none.
	SysCodeLeaseBlockerReturned = "lease_blocker_returned"
	// SysCodeLeaseHandoff — turn handed to the assignee after a non-assignee
	// comment. params: {"comment_id": "<id>"}.
	SysCodeLeaseHandoff = "lease_handoff"
	// SysCodeWithdrawalBlockedRawArmed / ...ReaffirmPending — advice codes for
	// a refused gate withdrawal (task_handler withdrawalBlockedAdvice reasons).
	// params: none.
	SysCodeWithdrawalBlockedRawArmed        = "raw_arm"
	SysCodeWithdrawalBlockedReaffirmPending = "reaffirm_pending"
)

// SystemCodes lists every code above; the test keeps it in step with the
// constants and checks the values are unique.
var SystemCodes = []string{
	SysCodeMovedToTriage, SysCodeSoftGateParked, SysCodeSecretRotationSuppressed,
	SysCodeEndpointGateHint, SysCodeAPIArmHint, SysCodeGateArmedRaw, SysCodeGateArmedHard,
	SysCodeGateClearedManual, SysCodeGateClearedOwnArm, SysCodeGateReleasedHumanReply,
	SysCodeGateReleasedWithdrawn, SysCodeGateStillRaised, SysCodeGateWithdrawalConflict,
	SysCodeTriageExit, SysCodeReleaseSoftTimeout, SysCodeMonitorUnparked,
	SysCodeUmbrellaAutoClosed, SysCodeClosedTaskFollowupPending,
	SysCodeClosedTaskFollowupEscalated, SysCodeClosedTaskFollowupNotice,
	SysCodeClosedTaskRepeatDeferred, SysCodeRepeatComment, SysCodeAssigneeAutoChanged,
	SysCodeRecurringRepeat, SysCodeRecurringMissed, SysCodeLeaseParked,
	SysCodeLeaseBlockerReturned, SysCodeLeaseHandoff,
	SysCodeWithdrawalBlockedRawArmed, SysCodeWithdrawalBlockedReaffirmPending,
}
