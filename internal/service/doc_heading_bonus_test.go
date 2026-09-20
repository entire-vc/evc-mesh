package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func TestHeadingMatchFraction(t *testing.T) {
	cases := []struct {
		query, heading string
		want           float64
	}{
		{"аудит флота гейт предикат", "3.5 Рекомендации по гейтам и оркестрации — с оценкой", 0.25}, // гейт ~ гейтам
		{"аудит флота гейт предикат", "Аудит флота 06.09.2026: диагноз", 0.5},                       // аудит, флот(а)
		{"аудит флота гейт предикат", "Совсем другое", 0},
		{"рекомендации по гейтам и оркестрации", "3.5 Рекомендации по гейтам и оркестрации — с оценкой", 1}, // short words (по, и) do not count
		{"", "anything", 0},
		{"abc", "abc", 0}, // a token under 4 runes is not evidence
		{"recall latency", "Recall latency baseline", 1},
	}
	for _, c := range cases {
		assert.InDelta(t, c.want, headingMatchFraction(c.query, c.heading), 1e-9, "%q vs %q", c.query, c.heading)
	}
}

func TestDocMaxSlots(t *testing.T) {
	// The float product 15*0.2 is 3.0000000000000004; a naive Ceil would give 4.
	for limit, want := range map[int]int{1: 1, 5: 1, 6: 2, 10: 2, 15: 3, 20: 4, 50: 10} {
		assert.Equal(t, want, docMaxSlots(limit), "limit %d", limit)
	}
}

func headedDocHit(slug, heading string, score float64) domain.ScoredMemory {
	h := docHit(slug, score)
	h.DocHeading = heading
	return h
}

func TestRecall_DocArm_ShareIsCappedAtTwentyPercent(t *testing.T) {
	var hits []domain.ScoredMemory
	for i := 0; i < 20; i++ {
		hits = append(hits, docHit("d", float64(20-i)))
	}
	assert.Len(t, recallWithDocs(t, true, &fakeDocChunkRepo{hits: hits}, nil, 5), 1, "limit 5 -> one doc slot")
	assert.Len(t, recallWithDocs(t, true, &fakeDocChunkRepo{hits: hits}, nil, 10), 2, "limit 10 -> two doc slots")
}

func TestRecall_DocArm_HeadingEchoingTheQueryOutranksABetterArmRank(t *testing.T) {
	// The chunk whose heading repeats the query's words sits BELOW an unrelated chunk
	// in the arm ranking; the heading bonus must lift it above.
	unrelated := headedDocHit("unrelated", "Something else entirely", 9)
	echo := headedDocHit("echo", "Audit recommendations", 1)
	docs := &fakeDocChunkRepo{hits: []domain.ScoredMemory{unrelated, echo}}
	repo := &mockMemoryRepo{}
	svc := NewMemoryService(repo, &mockMemoryEdgeRepo{}, nil, MemoryWithDocIndex(docs, true))
	got, _, err := svc.Recall(context.Background(), domain.RecallOpts{Query: "audit recommendations", WorkspaceID: uuid.New(), Limit: 10, DocViewer: allDocs()})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "echo", got[0].DocSlug, "the heading that echoes the query goes first")
}

func TestRecall_DocArm_AsksForAWiderPoolThanMemories(t *testing.T) {
	docs := &fakeDocChunkRepo{}
	recallWithDocs(t, true, docs, nil, 5)
	assert.GreaterOrEqual(t, docs.lastLimit, 100,
		"the heading bonus can only reorder candidates the arm returned; a pool of limit*3 never contained the target chunk")
}

func TestRecall_MemoriesAreNotGivenAHeadingBonus(t *testing.T) {
	// A memory whose KEY equals the query must not be reordered by the doc bonus.
	a := domain.ScoredMemory{Memory: domain.Memory{ID: uuid.New(), Key: "audit recommendations", Content: "c", ImportanceScore: 0.8}, Score: 0.5}
	b := domain.ScoredMemory{Memory: domain.Memory{ID: uuid.New(), Key: "other", Content: "c", ImportanceScore: 0.8}, Score: 0.9}
	got := recallWithDocs(t, true, &fakeDocChunkRepo{}, []domain.ScoredMemory{b, a}, 10)
	require.Len(t, got, 2)
	assert.Equal(t, "other", got[0].Key)
}
