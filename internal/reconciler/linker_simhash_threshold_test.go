package reconciler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/simhash"
)

// The linker's simhash signal on the current simhash.

// linkerCorpusUnrelatedMaxShare is the most unrelated corpus pairs that may get
// the linker's simhash signal. The current algorithm gives 4 of 1225 (0.33%);
// the pre-fix FNV-64a gave most of them (median distance of unrelated texts 15
// bits), which made the signal decide supersedes on its own.
const linkerCorpusUnrelatedMaxShare = 0.01

// loadLinkerCorpus reads the linker corpus: 25 en + 25 ru unrelated texts of
// 500+ characters, separated by "\n---\n".
func loadLinkerCorpus(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, lang := range []string{"en", "ru"} {
		raw, err := os.ReadFile(filepath.Join("..", "service", "testdata", "simhash_corpus_"+lang+".txt"))
		if err != nil {
			t.Fatalf("read corpus: %v", err)
		}
		for _, part := range strings.Split(string(raw), "\n---\n") {
			if part = strings.TrimSpace(part); part != "" {
				out[lang] = append(out[lang], part)
			}
		}
	}
	if len(out["en"]) != 25 || len(out["ru"]) != 25 {
		t.Fatalf("corpus must hold 25 en + 25 ru texts, got en=%d ru=%d", len(out["en"]), len(out["ru"]))
	}
	return out
}

func TestLinkerSimhashMaxBits_Corpus(t *testing.T) {
	corpus := loadLinkerCorpus(t)
	var texts []string
	var langs []string
	for _, lang := range []string{"en", "ru"} {
		for _, s := range corpus[lang] {
			texts = append(texts, s)
			langs = append(langs, lang)
		}
	}
	hashes := make([]int64, len(texts))
	for i, s := range texts {
		hashes[i] = simhash.Compute(s)
	}

	t.Run("few unrelated pairs get the simhash signal", func(t *testing.T) {
		within, total := 0, 0
		for i := range texts {
			for j := i + 1; j < len(texts); j++ {
				total++
				if simhash.HammingDistance(hashes[i], hashes[j]) <= LinkerSimhashMaxBits {
					within++
				}
			}
		}
		share := float64(within) / float64(total)
		t.Logf("unrelated pairs within %d bits: %d of %d (%.2f%%)", LinkerSimhashMaxBits, within, total, 100*share)
		if total != 1225 {
			t.Fatalf("compared %d pairs, want 1225", total)
		}
		if share > linkerCorpusUnrelatedMaxShare {
			t.Errorf("unrelated pairs within %d bits: %d of %d (%.2f%%), want <= %.0f%%",
				LinkerSimhashMaxBits, within, total, 100*share, 100*linkerCorpusUnrelatedMaxShare)
		}
	})

	t.Run("every 1-2 word edit gets the simhash signal", func(t *testing.T) {
		repl1 := map[string]string{"en": "corrected", "ru": "исправлено"}
		repl2 := map[string]string{"en": "updated", "ru": "обновлено"}
		worst := 0
		for i, s := range texts {
			words := strings.Fields(s)
			words[len(words)/2] = repl1[langs[i]]
			one := strings.Join(words, " ")
			words[len(words)/4] = repl2[langs[i]]
			two := strings.Join(words, " ")
			for _, v := range []string{one, two} {
				d := simhash.HammingDistance(hashes[i], simhash.Compute(v))
				if d > worst {
					worst = d
				}
				if d > LinkerSimhashMaxBits {
					t.Errorf("edit of text %d (%s): %d bits > %d", i, langs[i], d, LinkerSimhashMaxBits)
				}
			}
		}
		t.Logf("worst 1-2 word edit: %d bits", worst)
	})
}

// RepairLinkerSimhashSupersedes (postgres) decides "the linker would supersede"
// as "at least three of the four signals". That holds only while every
// three-signal combination reaches autoActThreshold and no two-signal one does.
func TestComputeConfidence_ThreeOfFourSignals(t *testing.T) {
	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	for mask := 0; mask < 16; mask++ {
		prefix, tags, hash, hour := mask&1 != 0, mask&2 != 0, mask&4 != 0, mask&8 != 0
		older := domain.Memory{Key: "alpha:one:x", Tags: pq.StringArray{"a"}, ContentSimhash: ptrInt64(0), CreatedAt: base}
		newer := domain.Memory{Key: "beta:two:y", Tags: pq.StringArray{"b"}, ContentSimhash: ptrInt64(0x7FF), CreatedAt: base.Add(30 * time.Minute)}
		if prefix {
			newer.Key = "alpha:one:y"
		}
		if tags {
			newer.Tags = pq.StringArray{"a"}
		}
		if hash {
			newer.ContentSimhash = ptrInt64(0x3FF) // exactly LinkerSimhashMaxBits away
		}
		if hour {
			newer.CreatedAt = base.Add(61 * time.Minute)
		}
		signals := 0
		for _, b := range []bool{prefix, tags, hash, hour} {
			if b {
				signals++
			}
		}
		got := computeConfidence(older, newer) >= autoActThreshold
		if got != (signals >= 3) {
			t.Errorf("prefix=%v tags=%v hash=%v hour=%v: conf=%v, supersede=%v, want %v",
				prefix, tags, hash, hour, computeConfidence(older, newer), got, signals >= 3)
		}
	}
}
