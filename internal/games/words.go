package games

import (
	_ "embed"
	"strings"
	"sync"
)

// Embedded language assets. These ship inside the binary so word
// validation works with zero external calls and zero latency.
//
//	words_en.txt    — 452,847 lowercase English words (a-z), one per line.
//	                  Dictionary validation (Word Chain) + "is this a real
//	                  word" checks.
//	cefr.tsv        — 8,818 "word<TAB>level" (A1..C2), curated CEFR labels.
//	freq_levels.tsv — frequency-derived CEFR level for words NOT in cefr.tsv,
//	                  so modern/technical words (smartphone, algorithm) score
//	                  fairly instead of always 1. cefr.tsv stays authoritative.
//
//go:embed assets/words_en.txt
var dictRaw string

//go:embed assets/cefr.tsv
var cefrRaw string

//go:embed assets/freq_levels.tsv
var freqLevelsRaw string

var (
	loadOnce sync.Once
	dict     map[string]struct{}
	cefr     map[string]string
	freq     map[string]string // frequency-derived levels (fills the cefr gap)
)

func load() {
	loadOnce.Do(func() {
		lines := strings.Split(dictRaw, "\n")
		dict = make(map[string]struct{}, len(lines))
		for _, w := range lines {
			w = strings.TrimSpace(w)
			if w != "" {
				dict[w] = struct{}{}
			}
		}

		clines := strings.Split(cefrRaw, "\n")
		cefr = make(map[string]string, len(clines))
		for _, line := range clines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, "\t", 2)
			if len(parts) == 2 {
				cefr[parts[0]] = parts[1]
			}
		}

		flines := strings.Split(freqLevelsRaw, "\n")
		freq = make(map[string]string, len(flines))
		for _, line := range flines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, "\t", 2)
			if len(parts) == 2 {
				freq[parts[0]] = parts[1]
			}
		}
	})
}

// Warm preloads the embedded dictionary + CEFR maps (and the question
// banks) so the FIRST mini-game doesn't pay the one-time parse cost on
// the request path. Safe to call repeatedly (sync.Once guards both) and
// from a goroutine; main wires `go games.Warm()` at startup.
func Warm() {
	load()
	loadBanks()
	loadVectors()
	loadProfanity()
	RefreshTopicCache()
	RefreshSynonymCache()
	RefreshDictationPool()
}

// normalizeWord lowercases and trims a candidate word. Returns the
// cleaned form and whether it's a single alphabetic token (a-z only).
func normalizeWord(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", false
	}
	for _, r := range s {
		if r < 'a' || r > 'z' {
			return s, false
		}
	}
	return s, true
}

// IsWord reports whether s is a valid single English word in our
// dictionary. Input is normalised (lowercased/trimmed) first.
func IsWord(s string) bool {
	load()
	w, ok := normalizeWord(s)
	if !ok {
		return false
	}
	_, found := dict[w]
	return found
}

// CEFRLevel returns the CEFR level ("A1".."C2") for a word - curated
// (cefr.tsv) or, failing that, frequency-derived (freq_levels.tsv) - or ""
// if neither source has it.
func CEFRLevel(s string) string {
	load()
	w, _ := normalizeWord(s)
	return levelOf(w)
}

// levelOf returns a word's CEFR level: the curated cefr.tsv label if present,
// otherwise the frequency-derived freq_levels.tsv label, otherwise "".
func levelOf(w string) string {
	if lvl, ok := cefr[w]; ok {
		return lvl
	}
	return freq[w]
}

// CEFRPoints maps a word's difficulty level (from levelOf) to a score:
//
//	A1/A2 → 1   (basic)
//	B1/B2 → 2   (intermediate)
//	C1/C2 → 4   (advanced)
//
// Level comes from cefr.tsv, falling back to frequency (freq_levels.tsv). A
// valid word with no level at all scores 1; a non-word scores 0.
func CEFRPoints(s string) int {
	load()
	w, ok := normalizeWord(s)
	if !ok {
		return 0
	}
	switch levelOf(w) {
	case "A1", "A2":
		return 1
	case "B1", "B2":
		return 2
	case "C1", "C2":
		return 4
	}
	if _, found := dict[w]; found {
		return 1
	}
	return 0
}
