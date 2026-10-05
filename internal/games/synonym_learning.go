package games

import (
	"sync"

	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/safego"
)

// In-memory cache of AI-accepted synonym/antonym answers that are NOT in the
// embedded bank, keyed by (word,type) -> set of accepted answers. Lets newly
// learned answers count as correct without rebuilding the binary. Refreshed at
// startup and after the weekly cleanup. Read on the hot path (Move), so it's
// kept in memory - no DB query per answer.
var (
	synCacheMu  sync.RWMutex
	acceptedSyn map[string]map[string]struct{}
)

func synKey(word, typ string) string { return word + "\x00" + typ }

// RefreshSynonymCache reloads accepted candidates from synonym_candidates.
// Safe on an empty table (cold start): the cache just stays empty.
func RefreshSynonymCache() {
	cache := map[string]map[string]struct{}{}
	if database.DB != nil {
		var rows []models.SynonymCandidate
		database.DB.Where("status = ?", models.SynonymAccepted).Find(&rows)
		for _, r := range rows {
			k := synKey(r.Word, r.Type)
			if cache[k] == nil {
				cache[k] = map[string]struct{}{}
			}
			cache[k][r.Answer] = struct{}{}
		}
	}
	synCacheMu.Lock()
	acceptedSyn = cache
	synCacheMu.Unlock()
}

// isAcceptedSynonym reports whether `answer` (already normAnswer'd) was AI-
// accepted as a synonym/antonym of `word`.
func isAcceptedSynonym(word, typ, answer string) bool {
	synCacheMu.RLock()
	defer synCacheMu.RUnlock()
	m := acceptedSyn[synKey(word, typ)]
	if m == nil {
		return false
	}
	_, ok := m[answer]
	return ok
}

// synonymWrong is one "not in the bank" answer captured during a duel.
type synonymWrong struct {
	word, typ, answer string
}

// recordSynonymCandidates upserts captured wrong answers into
// synonym_candidates for the weekly AI to review. Runs off the game lock.
func recordSynonymCandidates(items []synonymWrong) {
	if len(items) == 0 || database.DB == nil {
		return
	}
	seen := map[string]struct{}{}
	var uniq []synonymWrong
	for _, it := range items {
		k := it.word + "|" + it.typ + "|" + it.answer
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		uniq = append(uniq, it)
	}
	safego.Go("games.recordSynonymCandidates", func() {
		for _, it := range uniq {
			database.DB.Exec(`
				INSERT INTO synonym_candidates (id, word, type, answer, uses, status, created_at, updated_at)
				VALUES (gen_random_uuid(), ?, ?, ?, 1, 'pending', NOW(), NOW())
				ON CONFLICT (word, type, answer) DO UPDATE
				SET uses = synonym_candidates.uses + 1, updated_at = NOW()`,
				it.word, it.typ, it.answer)
		}
	})
}
