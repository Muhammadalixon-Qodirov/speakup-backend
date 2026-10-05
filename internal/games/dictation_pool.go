package games

import (
	"strings"
	"sync"

	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
)

// AI-generated dictation sentences loaded from the DB, kept in memory so the
// game picks sentences without a DB query. The embedded seed bank (dictDB) is
// always available; this just adds the AI-grown ones on top.
var (
	dictAIMu sync.RWMutex
	dictAI   []dictationS
)

// RefreshDictationPool reloads AI-generated sentences from the DB. Safe on an
// empty table (cold start): only the embedded seed bank is used.
func RefreshDictationPool() {
	var rows []models.DictationSentence
	if database.DB != nil {
		database.DB.Find(&rows)
	}
	pool := make([]dictationS, 0, len(rows))
	for _, r := range rows {
		pool = append(pool, dictationS{Text: r.Text, Level: r.Level})
	}
	dictAIMu.Lock()
	dictAI = pool
	dictAIMu.Unlock()
}

// dictationAll returns the full Dictation Race pool: embedded seed + AI-grown.
func dictationAll() []dictationS {
	loadBanks()
	dictAIMu.RLock()
	ai := dictAI
	dictAIMu.RUnlock()
	all := make([]dictationS, 0, len(dictDB)+len(ai))
	all = append(all, dictDB...)
	all = append(all, ai...)
	return all
}

// DictationCount reports how many sentences are available (seed + AI). Used by
// the replenish job to decide whether to generate more.
func DictationCount() int {
	loadBanks()
	dictAIMu.RLock()
	n := len(dictDB) + len(dictAI)
	dictAIMu.RUnlock()
	return n
}

// DictationTexts returns the set of normalised texts already in the pool, so
// the generator can skip duplicates.
func DictationTexts() map[string]struct{} {
	all := dictationAll()
	set := make(map[string]struct{}, len(all))
	for _, s := range all {
		set[strings.ToLower(strings.TrimSpace(s.Text))] = struct{}{}
	}
	return set
}
