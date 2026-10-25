package service

import (
	"regexp"
	"strings"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// LabelPostMergeAC marks a task whose acceptance needs a run after its MR is
// merged, for descriptions the phrase list below does not recognise.
const LabelPostMergeAC = "post-merge-ac"

// postMergeACPattern finds acceptance criteria that can only be met after the
// merge: a live run, a check on main or prod after deploy. an internal ticket: the
// "merged → review" rule moved an internal ticket to review three times in one day
// while its AC2 (a live agent-chat-e2e run after deploy) was still unmet; in
// review the executor's lane stops feeding the card (review_refeed_hold), so
// the card sat idle until someone pulled it back by hand.
//
// A false positive only leaves the card with its executor in in_progress, who
// moves it on after the check; a false negative is the bug above. So the list
// errs towards matching.
var postMergeACPattern = regexp.MustCompile(`(?i)` +
	`post[- ]?(merge|deploy)` +
	`|after (the )?(merge|deploy|rollout|release)` +
	`|live (run|check|probe|acceptance|test)` +
	`|после (мёржа|мержа|мерджа|merge|слияния|деплоя|выкатки|раскатки|релиза)` +
	`|жив(ой|ая|ое|ым|ую) (прогон|проверк|приёмк|приемк|probe|e2e)`)

// postMergeACMatch returns the phrase that marks the task as needing a
// post-merge check, or "" when it does not.
func postMergeACMatch(t *domain.Task) string {
	if t == nil {
		return ""
	}
	for _, l := range t.Labels {
		if strings.EqualFold(l, LabelPostMergeAC) {
			return "label " + LabelPostMergeAC
		}
	}
	return postMergeACPattern.FindString(t.Description)
}
