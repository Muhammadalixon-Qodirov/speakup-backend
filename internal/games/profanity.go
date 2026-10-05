package games

import (
	_ "embed"
	"strings"
	"sync"
)

// Embedded profanity / slur blocklist (LDNOOBW, single-token a-z entries).
// Used to keep offensive words out of the word games - they must never score
// or appear on the result screen of a learning app aimed at students.
//
//go:embed assets/profanity.txt
var profanityRaw string

var (
	profOnce sync.Once
	profSet  map[string]struct{}
)

func loadProfanity() {
	profOnce.Do(func() {
		profSet = make(map[string]struct{})
		for _, w := range strings.Split(profanityRaw, "\n") {
			w = strings.ToLower(strings.TrimSpace(w))
			if w != "" {
				profSet[w] = struct{}{}
			}
		}
	})
}

// IsProfane reports whether a word is in the embedded profanity blocklist.
func IsProfane(word string) bool {
	loadProfanity()
	_, bad := profSet[strings.ToLower(strings.TrimSpace(word))]
	return bad
}
