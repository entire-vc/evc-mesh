package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ── stub mid-pipeline rules source ─────────────────────────────────────────

// The embedded RulesService is deliberately a nil interface: only
// GetProjectWorkflowRules is exercised by the evidence gate, and any other
// method reached from here would panic loudly rather than quietly returning a
// zero value that could make a broken test look green.
type stubRulesSvc struct {
	RulesService
	cfg *domain.MidPipelineConfig
	err error
}

func (s *stubRulesSvc) GetProjectWorkflowRules(_ context.Context, _ uuid.UUID, _ *uuid.UUID) (*domain.WorkflowRulesResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &domain.WorkflowRulesResponse{
		WorkflowRulesConfig: domain.WorkflowRulesConfig{MidPipeline: s.cfg},
	}, nil
}

// evidenceHarness builds a taskService with only the pieces the review-evidence
// gate touches, so a failure here cannot be a symptom of unrelated wiring.
type evidenceHarness struct {
	svc         *taskService
	commentRepo *MockCommentRepository
	task        *domain.Task
}

func newEvidenceHarness(strict bool) *evidenceHarness {
	return newEvidenceHarnessWithRules(&stubRulesSvc{
		cfg: &domain.MidPipelineConfig{ReviewEvidenceStrict: strict},
	})
}

func newEvidenceHarnessWithRules(rules *stubRulesSvc) *evidenceHarness {
	cr := NewMockCommentRepository()
	var rs RulesService
	if rules != nil {
		rs = rules
	}
	return &evidenceHarness{
		svc:         &taskService{commentRepo: cr, rulesConfigSvc: rs},
		commentRepo: cr,
		task: &domain.Task{
			ID:        uuid.New(),
			ProjectID: uuid.New(),
			DodChecks: domain.DodChecks{},
		},
	}
}

func (h *evidenceHarness) comment(body string) {
	_ = h.commentRepo.Create(context.Background(), &domain.Comment{
		ID:        uuid.New(),
		TaskID:    h.task.ID,
		Body:      body,
		CreatedAt: time.Now(),
	})
}

func (h *evidenceHarness) passes() bool {
	ok, _ := h.svc.passesReviewEvidenceGate(context.Background(), h.task)
	return ok
}

// strictReported is what the 422 will claim decided the refusal. It must match
// the condition that actually ran.
func (h *evidenceHarness) strictReported() bool {
	_, strict := h.svc.passesReviewEvidenceGate(context.Background(), h.task)
	return strict
}

// ── the loose gate must not change for anyone who has not opted in ─────────

// The single most important test in this file. Strict mode is opt-in per
// project; if turning it on somewhere silently changed behaviour everywhere,
// the flag would be worthless as a blast-radius control. A comment with no URL
// is exactly the case the two conditions disagree on, so it is the case that
// proves the default arm is still the old one.
func TestReviewEvidence_DefaultOff_AnyCommentStillPasses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rules *stubRulesSvc
	}{
		{"no mid_pipeline block at all", &stubRulesSvc{cfg: nil}},
		{"block present, flag false", &stubRulesSvc{cfg: &domain.MidPipelineConfig{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newEvidenceHarnessWithRules(tc.rules)
			h.comment("начал работу, разбираюсь")
			if !h.passes() {
				t.Fatal("loose gate refused a plain comment — this is the pre-existing behaviour and must not change for projects that have not opted in")
			}
		})
	}
}

// Nil rules service: the gate must still work, and still loosely.
func TestReviewEvidence_NoRulesService_StaysLoose(t *testing.T) {
	h := newEvidenceHarnessWithRules(nil)
	h.comment("no url here")
	if !h.passes() {
		t.Fatal("gate went strict with no rules service wired — a project cannot have opted into anything")
	}
}

// ── strict mode: the refusal it exists to produce ─────────────────────────

func TestReviewEvidence_Strict_PlainCommentRefused(t *testing.T) {
	h := newEvidenceHarness(true)
	h.comment("сделал, всё работает, тесты прошли")
	if h.passes() {
		t.Fatal("strict gate accepted a comment with no URL — this is the vacuous behaviour the flag exists to remove")
	}
}

func TestReviewEvidence_Strict_NoCommentsAtAllRefused(t *testing.T) {
	h := newEvidenceHarness(true)
	if h.passes() {
		t.Fatal("strict gate accepted a task with no evidence whatsoever")
	}
}

// ── strict mode: each accepting arm, one at a time ────────────────────────

func TestReviewEvidence_Strict_CommentWithURLPasses(t *testing.T) {
	for _, body := range []string{
		"MR: https://git.entire.host/entire-vc/evc-mesh/-/merge_requests/12",
		"пайплайн зелёный, лог http://ci.internal/job/9",
		"see HTTPS://EXAMPLE.COM/proof for the run",
		"multi\nline\nwith https://x.test/y buried in it",
	} {
		t.Run(shortName(body), func(t *testing.T) {
			h := newEvidenceHarness(true)
			h.comment(body)
			if !h.passes() {
				t.Fatalf("strict gate refused a comment carrying a URL: %q", body)
			}
		})
	}
}

func TestReviewEvidence_Strict_ArtifactPasses(t *testing.T) {
	h := newEvidenceHarness(true)
	h.task.ArtifactCount = 1
	if !h.passes() {
		t.Fatal("strict gate refused a task with an uploaded artifact")
	}
}

func TestReviewEvidence_Strict_VCSLinkPasses(t *testing.T) {
	h := newEvidenceHarness(true)
	h.task.VCSLinkCount = 1
	if !h.passes() {
		t.Fatal("strict gate refused a task with a linked MR/PR")
	}
}

func TestReviewEvidence_Strict_PassingDodCheckPasses(t *testing.T) {
	h := newEvidenceHarness(true)
	h.task.DodChecks = domain.DodChecks{"ci": {Status: domain.DodCheckPass, UpdatedAt: time.Now()}}
	if !h.passes() {
		t.Fatal("strict gate refused a task with a passing dod_check")
	}
}

// The negative control on the dod_checks arm, and the reason it is written as
// "at least one PASSING" rather than "the map is non-empty": a pending check has
// not answered and a failing one answered no. If mere presence counted, a caller
// could satisfy an evidence gate by declaring gates it never ran — evidence of
// intent rather than of result.
func TestReviewEvidence_Strict_PendingOrFailingDodChecksAreNotEvidence(t *testing.T) {
	for name, checks := range map[string]domain.DodChecks{
		"pending only": {"ci": {Status: domain.DodCheckPending}},
		"failing only": {"ci": {Status: domain.DodCheckFail}},
		"both, neither passing": {
			"ci":   {Status: domain.DodCheckFail},
			"e2e":  {Status: domain.DodCheckPending},
			"lint": {Status: domain.DodCheckPending},
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newEvidenceHarness(true)
			h.task.DodChecks = checks
			h.comment("no url")
			if h.passes() {
				t.Fatalf("strict gate accepted %s as evidence — only a PASSING check is a result", name)
			}
		})
	}
}

// ── fail-open on an unreadable probe ──────────────────────────────────────

// "We could not look" must not be reported as "we looked and there was nothing".
// The gate refuses transitions for the whole fleet; a comment-store blip must not
// become a fleet-wide stall.
func TestReviewEvidence_Strict_CommentProbeErrorFailsOpen(t *testing.T) {
	h := newEvidenceHarness(true)
	h.commentRepo.errToReturn = errors.New("comment store unavailable")
	if !h.passes() {
		t.Fatal("strict gate refused on a comment-probe ERROR — an unreadable probe must fail open, not block")
	}
}

// Same reasoning one level up: an unreadable workflow config must leave the
// pre-existing (loose) behaviour in place rather than either blocking or
// silently going strict.
func TestReviewEvidence_RulesReadErrorStaysLoose(t *testing.T) {
	h := newEvidenceHarnessWithRules(&stubRulesSvc{err: errors.New("rules unavailable")})
	h.comment("plain comment, no url")
	if !h.passes() {
		t.Fatal("a rules-read error changed the gate's behaviour; it must degrade to the pre-existing loose form")
	}
}

// ── the 422 must say what would actually pass ─────────────────────────────

// Under strict mode the loose message is not merely vaguer, it is wrong: it
// tells the caller a comment with proof is enough while the gate is counting
// URLs, so following it earns a second identical refusal.
func TestReviewEvidenceError_StrictMessageNamesTheStrictConditions(t *testing.T) {
	loose := (&ReviewEvidenceError{Strict: false}).Error()
	strict := (&ReviewEvidenceError{Strict: true}).Error()

	if loose == strict {
		t.Fatal("strict and loose refusals produce the same message; the caller cannot tell what would satisfy the gate")
	}
	for _, want := range []string{"dod_check", "URL"} {
		if !strings.Contains(strict, want) {
			t.Errorf("strict message does not mention %q: %s", want, strict)
		}
	}
	if strings.Contains(loose, "dod_check") {
		t.Errorf("loose message leaked a strict-only condition: %s", loose)
	}
}

// shortName trims a body down to something readable as a subtest name.
func shortName(s string) string {
	const n = 24
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// The refusal must not describe a condition other than the one that refused: a
// loose refusal rendered with the strict message (or the reverse) sends the
// caller to satisfy a gate that is not the one blocking them.
func TestReviewEvidence_ReportedStrictnessMatchesTheConditionThatRan(t *testing.T) {
	strictH := newEvidenceHarness(true)
	strictH.comment("no url")
	if strictH.passes() || !strictH.strictReported() {
		t.Error("strict project: refused, but the refusal does not report itself as strict")
	}

	looseH := newEvidenceHarnessWithRules(&stubRulesSvc{cfg: nil})
	if looseH.passes() || looseH.strictReported() {
		t.Error("loose project: an evidence-less task must refuse, and report itself as loose")
	}
}
