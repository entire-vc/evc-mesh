package service

import (
	"strings"
	"unicode"
)

// docHeadingBonus is the ORDERING credit a chunk gets when its heading repeats ALL of the
// query's words (a partial match earns the matching fraction of it). It picks which docs
// take the doc slots and is never added to the score a doc competes with memories on. It
// is sized to about 1.5x a chunk that ranks first in both doc arms (~0.013):
// a section heading that echoes the query is the strongest relevance signal a doc chunk
// can carry, and BM25 over 'english'-stemmed Cyrillic text cannot see it (no Russian
// stemming, and an AND query with one absent word matches nothing).
const docHeadingBonus = 0.02

// docArmPool is how many candidates each doc arm returns. The heading bonus only
// reorders what the arms produced; with the memories' pool of limit*3 the section a
// user asked for could sit at BM25 rank 44 of 222 and never reach the fuse at all.
const docArmPool = 100

// headingTokens lowercases s and returns its distinct words of at least 4 runes.
// Shorter words ("по", "и", "of") are not evidence of a match.
func headingTokens(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	seen := make(map[string]struct{}, len(fields))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if len([]rune(f)) < 4 {
			continue
		}
		if _, dup := seen[f]; dup {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	return out
}

// headingMatchFraction is the share of the query's words that appear in the heading.
// A query word matches a heading word when one is a prefix of the other, which is the
// cheap stand-in for stemming the 'english' dictionary does not do for Russian:
// «гейт» matches «гейтам», «флота» matches «флот». Returns 0 for an empty query.
func headingMatchFraction(query, heading string) float64 {
	q := headingTokens(query)
	if len(q) == 0 {
		return 0
	}
	h := headingTokens(heading)
	matched := 0
	for _, qw := range q {
		for _, hw := range h {
			if strings.HasPrefix(hw, qw) || strings.HasPrefix(qw, hw) {
				matched++
				break
			}
		}
	}
	return float64(matched) / float64(len(q))
}
