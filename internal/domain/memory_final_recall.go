package domain

// FinalRecallOpts opts into the versioned final-selection contract. Limit and
// Offset describe the returned page, never the internal candidate budget.
type FinalRecallOpts struct {
	RecallOpts
	Graph bool
}

type FinalRecallItem struct {
	ScoredMemory
	GraphBoost     bool                  `json:"graph_boost,omitempty"`
	Provenance     RecallGraphProvenance `json:"provenance,omitempty"`
	HopDistance    int                   `json:"hop_distance,omitempty"`
	CompositeScore float64               `json:"composite_score,omitempty"`
}

type FinalRecallResult struct {
	Items           []FinalRecallItem
	Stats           RecallStats
	CandidateLimit  int
	GraphBoostCount int
}
