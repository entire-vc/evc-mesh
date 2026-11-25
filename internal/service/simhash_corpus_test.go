package service

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

// The corpus test pins the property consumers of content_simhash rely on:
// unrelated texts are far apart, and a note with a 1–2 word edit mostly stays
// close. A 64-bit simhash cannot keep every small edit of a 500–2000 character
// text within 3 bits without also bringing unrelated texts that close, so the
// edit property is a share (design decision): at least 65% of the edits
// within the near-dup threshold, every edit within simhashEditMaxBits.
// The corpus is 25 English and 25 Russian natural texts of 500+ characters on
// unrelated subjects (engineering notes, decisions, incidents and everyday
// prose). A few Russian texts are free translations of English ones on purpose:
// they share meaning but no character trigrams, so they must not look alike.
//
// Before the fix (bare FNV-64a per trigram) this test failed: FNV's low bits
// depend only on the low bits of the input bytes, so per-bit votes over a long
// text converged to the language's average and unrelated texts collapsed onto
// a handful of nearly identical hashes.

const (
	simhashCorpusMinRunes = 500
	// simhashNearDupBits is the threshold consumers use for "the same note,
	// rewritten" (U7 rewritten-miss classification).
	simhashNearDupBits = 3
	// simhashEditNearShare is the least share of 1–2 word edits within
	// simhashNearDupBits; U7 "rewritten" is therefore a lower bound.
	simhashEditNearShare = 0.65
	// simhashEditMaxBits bounds every 1–2 word edit.
	simhashEditMaxBits = 8
)

type corpusText struct {
	name string
	lang string
	text string
}

func loadSimhashCorpus(t *testing.T) []corpusText {
	t.Helper()
	var out []corpusText
	for _, lang := range []string{"en", "ru"} {
		path := filepath.Join("testdata", "simhash_corpus_"+lang+".txt")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read corpus %s: %v", path, err)
		}
		for i, part := range strings.Split(string(raw), "\n---\n") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			out = append(out, corpusText{name: lang + "#" + itoa2(i+1), lang: lang, text: part})
		}
	}
	return out
}

func itoa2(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// replaceWord returns text with the whitespace-separated word at index idx
// replaced by repl. The edit positions are fixed (middle and first quarter of
// the text), not chosen per text.
func replaceWord(text string, idx int, repl string) string {
	words := strings.Fields(text)
	words[idx] = repl
	return strings.Join(words, " ")
}

func TestComputeSimhash_Corpus(t *testing.T) {
	corpus := loadSimhashCorpus(t)

	perLang := map[string]int{}
	for _, c := range corpus {
		perLang[c.lang]++
		if n := utf8.RuneCountInString(c.text); n < simhashCorpusMinRunes {
			t.Fatalf("corpus text %s has %d runes, want >= %d", c.name, n, simhashCorpusMinRunes)
		}
	}
	if len(corpus) != 50 || perLang["en"] != 25 || perLang["ru"] != 25 {
		t.Fatalf("corpus must hold 25 en + 25 ru texts, got %v (total %d)", perLang, len(corpus))
	}

	hashes := make([]int64, len(corpus))
	for i, c := range corpus {
		hashes[i] = ComputeSimhash(c.text)
	}

	t.Run("unrelated pairs are farther than the near-dup threshold", func(t *testing.T) {
		var dists []int
		closePairs := 0
		for i := range corpus {
			for j := i + 1; j < len(corpus); j++ {
				d := HammingDistance(hashes[i], hashes[j])
				dists = append(dists, d)
				if d <= simhashNearDupBits {
					closePairs++
					t.Errorf("unrelated %s and %s: Hamming %d <= %d", corpus[i].name, corpus[j].name, d, simhashNearDupBits)
				}
			}
		}
		if len(dists) != 1225 {
			t.Fatalf("compared %d pairs, want 1225", len(dists))
		}
		sort.Ints(dists)
		t.Logf("pairs=%d min=%d p5=%d median=%d close=%d", len(dists), dists[0], dists[len(dists)/20], dists[len(dists)/2], closePairs)
	})

	t.Run("1-2 word edits mostly stay within the near-dup threshold", func(t *testing.T) {
		repl1 := map[string]string{"en": "corrected", "ru": "исправлено"}
		repl2 := map[string]string{"en": "updated", "ru": "обновлено"}
		worst, near, total := 0, 0, 0
		for i, c := range corpus {
			n := len(strings.Fields(c.text))
			one := replaceWord(c.text, n/2, repl1[c.lang])
			two := replaceWord(one, n/4, repl2[c.lang])
			for _, v := range []struct {
				label string
				text  string
			}{{"1-word", one}, {"2-word", two}} {
				d := HammingDistance(hashes[i], ComputeSimhash(v.text))
				total++
				if d <= simhashNearDupBits {
					near++
				}
				if d > worst {
					worst = d
				}
				if d > simhashEditMaxBits {
					t.Errorf("%s edit of %s: Hamming %d > %d", v.label, c.name, d, simhashEditMaxBits)
				}
			}
		}
		share := float64(near) / float64(total)
		if share < simhashEditNearShare {
			t.Errorf("edits within %d bits: %d of %d (%.0f%%), want >= %.0f%%",
				simhashNearDupBits, near, total, 100*share, 100*simhashEditNearShare)
		}
		t.Logf("edits within %d bits: %d/%d, worst edit distance = %d", simhashNearDupBits, near, total, worst)
	})
}
