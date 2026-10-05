package games

import (
	_ "embed"
	"math"
	"strconv"
	"strings"
	"sync"
)

// Embedded GloVe 50d word vectors for the Vocabulary Sprint topic categoriser.
// Only CEFR words (the scorable vocabulary) + topic seed words are included,
// so the file stays ~3MB instead of the full 400k-word model. Generated from
// glove-wiki-gigaword-50; format: "word<TAB>f1 f2 ... f50".
//
//go:embed assets/vocab_vectors.tsv
var vocabVectorsRaw string

// topicSeeds maps each Vocabulary Sprint topic to the seed words whose averaged
// (centroid) vector represents that topic. MUST stay in sync with the seeds
// used to generate assets/vocab_vectors.tsv (gen_vectors.py).
var topicSeeds = map[string][]string{
	"travel":            {"travel", "trip", "journey", "flight", "hotel", "tourist", "passport", "vacation", "airport", "luggage"},
	"food & cooking":    {"food", "cooking", "recipe", "kitchen", "meal", "restaurant", "chef", "cook", "delicious", "dinner"},
	"emotions":          {"emotion", "happy", "sad", "angry", "fear", "love", "feeling", "joy", "anxiety", "mood"},
	"business & work":   {"business", "work", "office", "company", "money", "manager", "job", "market", "salary", "career"},
	"nature & animals":  {"nature", "animal", "forest", "tree", "river", "wildlife", "mountain", "bird", "ocean", "plant"},
	"technology":        {"technology", "computer", "software", "internet", "digital", "device", "data", "phone", "app", "online"},
	"health & fitness":  {"health", "fitness", "exercise", "gym", "diet", "muscle", "workout", "healthy", "nutrition", "wellness"},
	"education":         {"education", "school", "student", "teacher", "learn", "study", "university", "class", "exam", "lesson"},
	"sport":             {"sport", "football", "game", "team", "player", "match", "athlete", "championship", "coach", "stadium"},
	"music & art":       {"music", "art", "song", "painting", "artist", "dance", "melody", "gallery", "guitar", "concert"},
	"city life":         {"city", "street", "building", "traffic", "urban", "downtown", "neighborhood", "apartment", "subway", "crowd"},
	"weather & seasons": {"weather", "rain", "snow", "summer", "winter", "sunny", "storm", "temperature", "cloud", "autumn"},
}

var (
	vecOnce   sync.Once
	wordVecs  map[string][]float32 // unit-normalised word vectors
	topicVecs map[string][]float32 // unit-normalised topic centroids
)

func loadVectors() {
	vecOnce.Do(func() {
		wordVecs = make(map[string][]float32, 9000)
		for _, line := range strings.Split(vocabVectorsRaw, "\n") {
			tab := strings.IndexByte(line, '\t')
			if tab < 0 {
				continue
			}
			fields := strings.Fields(line[tab+1:])
			v := make([]float32, len(fields))
			for i, f := range fields {
				val, _ := strconv.ParseFloat(f, 32)
				v[i] = float32(val)
			}
			normalizeVec(v)
			wordVecs[line[:tab]] = v
		}

		// Topic centroid = normalised mean of its seed-word vectors.
		topicVecs = make(map[string][]float32, len(topicSeeds))
		for topic, seeds := range topicSeeds {
			var sum []float32
			n := 0
			for _, s := range seeds {
				wv, ok := wordVecs[s]
				if !ok {
					continue
				}
				if sum == nil {
					sum = make([]float32, len(wv))
				}
				for i := range wv {
					sum[i] += wv[i]
				}
				n++
			}
			if n > 0 {
				normalizeVec(sum)
				topicVecs[topic] = sum
			}
		}
	})
}

// normalizeVec scales v to unit length in place (so cosine == dot product).
func normalizeVec(v []float32) {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	if s == 0 {
		return
	}
	inv := float32(1 / math.Sqrt(s))
	for i := range v {
		v[i] *= inv
	}
}

// topicSimilarity returns the cosine similarity (−1..1) between a word and a
// topic centroid, plus false when the word has no embedding (out-of-vocabulary)
// or the topic is unknown.
func topicSimilarity(topic, word string) (float64, bool) {
	loadVectors()
	wv, ok := wordVecs[word]
	if !ok {
		return 0, false
	}
	tv, ok := topicVecs[topic]
	if !ok {
		return 0, false
	}
	var dot float64
	for i := range wv {
		if i < len(tv) {
			dot += float64(wv[i]) * float64(tv[i])
		}
	}
	return dot, true
}
