package games

import (
	"strings"
	"sync"

	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/safego"
)

// Topic-relevance thresholds on cosine similarity to the topic centroid.
// Tunable; the weekly AI cleanup compensates for imperfect thresholds, so
// exact values aren't critical.
const (
	simOnTopic  = 0.40 // >= this -> clearly on-topic
	simMidTopic = 0.28 // >= this -> neutral; below -> off-topic

	// offTopicPoints is what a real word clearly unrelated to the topic earns.
	// Tiny on purpose - it isn't rejected, just barely rewarded, so spamming
	// off-topic words doesn't pay.
	offTopicPoints = 0.2
)

// In-memory caches of AI-reviewed words, keyed topic -> word. Trusted words
// are forced on-topic; rejected words are forced off-topic. Everything else
// falls back to live vector similarity. Refreshed at startup and after the
// weekly cleanup. Reading the hot path from memory keeps Move() DB-free.
var (
	topicCacheMu  sync.RWMutex
	trustedWords  map[string]map[string]struct{}
	rejectedWords map[string]map[string]struct{}
)

// RefreshTopicCache reloads the trusted/rejected caches from topic_words.
// Safe on an empty table (cold start): caches stay empty and runtime relies
// purely on vector similarity.
func RefreshTopicCache() {
	trusted := map[string]map[string]struct{}{}
	rejected := map[string]map[string]struct{}{}

	if database.DB != nil {
		var rows []models.TopicWord
		database.DB.
			Where("status IN ?", []string{models.TopicWordTrusted, models.TopicWordRejected}).
			Find(&rows)
		for _, r := range rows {
			dst := trusted
			if r.Status == models.TopicWordRejected {
				dst = rejected
			}
			if dst[r.Topic] == nil {
				dst[r.Topic] = map[string]struct{}{}
			}
			dst[r.Topic][r.Word] = struct{}{}
		}
	}

	topicCacheMu.Lock()
	trustedWords, rejectedWords = trusted, rejected
	topicCacheMu.Unlock()
}

// relevanceClass classifies a word for a topic as "on", "mid" or "off".
// Order: AI-rejected -> off; AI-trusted -> on; else vector similarity; OOV
// words default to "mid" (we don't punish words we can't judge).
func relevanceClass(topic, word string) string {
	topicCacheMu.RLock()
	if rw := rejectedWords[topic]; rw != nil {
		if _, bad := rw[word]; bad {
			topicCacheMu.RUnlock()
			return "off"
		}
	}
	if tw := trustedWords[topic]; tw != nil {
		if _, ok := tw[word]; ok {
			topicCacheMu.RUnlock()
			return "on"
		}
	}
	topicCacheMu.RUnlock()

	sim, ok := topicSimilarity(topic, word)
	if !ok {
		return "mid"
	}
	switch {
	case sim >= simOnTopic:
		return "on"
	case sim >= simMidTopic:
		return "mid"
	default:
		return "off"
	}
}

// topicWordPoints scores a word: the full CEFR difficulty (1/2/4) for words
// that fit the topic (on- or borderline-topic), or a tiny offTopicPoints
// (0.2) for words clearly unrelated. Soft - off-topic is never rejected,
// just barely rewarded, so it never pays to ignore the topic.
func topicWordPoints(topic, word string, base int) float64 {
	if relevanceClass(topic, word) == "off" {
		return offTopicPoints
	}
	return float64(base)
}

// recordTopicWords upserts every accepted word into topic_words for the
// self-learning loop. Runs OFF the game lock in a goroutine (called from the
// engine's finalize path, like persistResult).
func recordTopicWords(topic string, words []string) {
	if topic == "" || len(words) == 0 || database.DB == nil {
		return
	}
	type rec struct {
		word string
		sim  float64
	}
	seen := map[string]struct{}{}
	var recs []rec
	for _, w := range words {
		w = strings.ToLower(strings.TrimSpace(w))
		if w == "" {
			continue
		}
		if _, dup := seen[w]; dup {
			continue
		}
		seen[w] = struct{}{}
		sim, _ := topicSimilarity(topic, w)
		recs = append(recs, rec{w, sim})
	}
	if len(recs) == 0 {
		return
	}
	safego.Go("games.recordTopicWords", func() {
		for _, r := range recs {
			database.DB.Exec(`
				INSERT INTO topic_words (id, topic, word, uses, sum_sim, status, created_at, updated_at)
				VALUES (gen_random_uuid(), ?, ?, 1, ?, 'pending', NOW(), NOW())
				ON CONFLICT (topic, word) DO UPDATE
				SET uses = topic_words.uses + 1,
				    sum_sim = topic_words.sum_sim + EXCLUDED.sum_sim,
				    updated_at = NOW()`,
				topic, r.word, r.sim)
		}
	})
}
