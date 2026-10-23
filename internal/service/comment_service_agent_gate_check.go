package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Refusal codes for an agent-key arm of human_gate outside the marker path.
const (
	GateRefusalMarkerRequired   = "gate_marker_required"
	GateRefusalAddresseeMissing = "gate_addressee_required"
	GateRefusalAddresseeNotUser = "gate_addressee_not_human"
	GateRefusalRepeatAsk        = "repeat_ask_refused"
)

// GateMarkerHint is the one sentence every refusal ends with: the only way an agent
// asks a person is a marker line in a comment.
const GateMarkerHint = "To ask a person, post a comment whose own line is `❓ Blocking @<person>: <question>`."

// GateArmRefusedError is a refusal to arm human_gate for an agent caller. Code is stable
// and machine-readable (also the API error_code); Message tells the caller what to do.
type GateArmRefusedError struct {
	Code    string
	Message string
}

func (e *GateArmRefusedError) Error() string { return e.Code + ": " + e.Message }

// CheckAgentGateArm runs, for an agent key arming human_gate through set_human_gate
// (POST /human-gate), the same two checks the marker path runs in enforceBlockingTriage:
// the addressee resolves to a human user (not an agent, not an unknown name), and the
// previous ask on the thread has not already been answered unless `supersedes` is cited.
// Without it those checks existed only on the marker path, and a client with an agent key
// reached an armed gate that only a person can lift by going around them.
//
// A repeat-ask refusal also posts the same system notice the marker path posts, so the
// thread records why nothing was armed.
func (s *commentService) CheckAgentGateArm(ctx context.Context, taskID uuid.UUID, addressee, supersedes, canonicalKey string) error {
	slug := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(addressee), "@"))
	if slug == "" {
		return &GateArmRefusedError{GateRefusalAddresseeMissing,
			"addressee is required: the @name of the person being asked. " + GateMarkerHint}
	}
	if s.userRepo == nil || s.projectRepo == nil {
		return &GateArmRefusedError{GateRefusalMarkerRequired,
			"the gate check is unavailable, so the gate was not armed. " + GateMarkerHint}
	}
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return apierror.NotFound("Task")
	}
	proj, err := s.projectRepo.GetByID(ctx, task.ProjectID)
	if err != nil {
		return err
	}
	if proj == nil {
		return apierror.NotFound("Project")
	}
	user, err := s.userRepo.GetByUsername(ctx, proj.WorkspaceID, slug)
	if err != nil || user == nil {
		return &GateArmRefusedError{GateRefusalAddresseeNotUser,
			"@" + slug + " is not a person in this workspace (an agent or an unknown name). " +
				"Agents are woken by an assignment, not by a gate. " + GateMarkerHint}
	}

	probe := &domain.Comment{ID: uuid.Nil, TaskID: taskID}
	if k := strings.TrimSpace(canonicalKey); k != "" {
		probe.Metadata, _ = json.Marshal(map[string]string{"canonical_key": k})
	}
	// Same recorded-decision lookup as the marker path: a question that already has a
	// live decision under this key is not asked again.
	if existing := s.findLiveHumanGateDecision(ctx, taskID, probe); existing != nil {
		s.postExistingDecisionNotice(ctx, task, existing)
		return &GateArmRefusedError{GateRefusalRepeatAsk,
			"this question already has a recorded decision (canonical_key " + strings.TrimSpace(canonicalKey) + "). " +
				"Work from it, or ask about a new subject without that canonical_key."}
	}
	if strings.TrimSpace(supersedes) != "" {
		probe.Body = "supersedes: " + strings.TrimSpace(supersedes)
	}
	if !citesSupersedes(probe) {
		if ans := s.findAnswerSincePriorMarker(ctx, task, probe); ans != nil {
			s.postRepeatAskNotice(ctx, task, ans)
			return &GateArmRefusedError{GateRefusalRepeatAsk,
				"the previous question already has an answer (" + ans.at.Format("2006-01-02 15:04") + "): «" +
					clipQuote(strings.Join(strings.Fields(ans.quote), " "), 300) + "». " +
					"Work from it, or pass `supersedes` naming what the answer does not cover."}
		}
	}
	return nil
}

func clipQuote(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
