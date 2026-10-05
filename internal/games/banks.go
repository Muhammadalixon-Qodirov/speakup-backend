package games

import (
	_ "embed"
	"encoding/json"
	"math/rand"
	"sync"
)

// Embedded question banks for the round-based games. Curated, level-
// graded content that ships in the binary - controlled difficulty and
// zero external dependencies.

//go:embed assets/synonyms.json
var synonymsRaw []byte

//go:embed assets/dictation.json
var dictationRaw []byte

type synonymQ struct {
	Word    string   `json:"word"`
	Type    string   `json:"type"` // "synonym" | "antonym"
	Answers []string `json:"answers"`
}

type dictationS struct {
	Text  string `json:"text"`
	Level string `json:"level"`
}

var (
	banksOnce  sync.Once
	synonymsDB []synonymQ
	dictDB     []dictationS
)

func loadBanks() {
	banksOnce.Do(func() {
		_ = json.Unmarshal(synonymsRaw, &synonymsDB)
		_ = json.Unmarshal(dictationRaw, &dictDB)
	})
}

// pickIndices returns up to n distinct random indices into a slice of
// length total, in random order. If n >= total it returns all indices
// shuffled.
func pickIndices(total, n int) []int {
	if n > total {
		n = total
	}
	perm := rand.Perm(total)
	return perm[:n]
}
