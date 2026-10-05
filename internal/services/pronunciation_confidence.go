package services

import "strings"

// Confidence policy: which phoneme substitutions we are willing to call an
// error to the learner's face.
//
// Measured, not assumed. Running the sidecar over four human-annotated
// speechocean762 recordings (22 words every annotator marked correct) produced
// five "xato" verdicts - a 23% false-positive rate with zero true positives.
// The published literature says feedback below roughly 66% accuracy makes
// learners worse, not better, so shipping the raw verdict would do harm.
//
// Every one of those five had the same shape - the recogniser was right about
// the acoustics and wrong about what it implies:
//
//	WIN        w -> u    [w] and [u] are nearly the same sound
//	IMPORTANT  m -> n    assimilation before /p/, normal English
//	IMPORTANT  ɹ -> ʊ    r-vocalisation, normal English
//	LAST       l -> n    recogniser confusion
//	DO         d -> t    onset aspiration, and reported as if word-final
//	WAS        z -> s    function-word reduction before a vowel
//
// So an error is only reported when the substitution is one learners with this
// first language actually make AND is not on the confusable list. Everything
// else is downgraded to a hint: still shown, still playable against the
// reference, but not presented as a mistake.

// highConfidenceErrors are substitutions worth reporting: the documented
// Uzbek/Russian L1 substitutions, which are also acoustically far apart enough
// that the recogniser is unlikely to invent them.
var highConfidenceErrors = map[[2]string]struct{}{
	// /θ/ -> [s] or [t]: think -> sink / tink
	{"θ", "s"}: {}, {"θ", "t"}: {}, {"θ", "f"}: {},
	// /ð/ -> [z] or [d]: this -> zis / dis
	{"ð", "z"}: {}, {"ð", "d"}: {},
	// /w/ <-> /v/: west -> vest. Note [w]->[u] is NOT here; that is the
	// recogniser hearing the same sound two ways.
	{"w", "v"}: {}, {"v", "w"}: {}, {"v", "f"}: {},
}

// confusablePairs are substitutions the recogniser produces on correct speech,
// or that are normal English variation. These never become errors even when the
// sounds differ, and they outrank the final-devoicing rule below.
var confusablePairs = map[[2]string]struct{}{
	{"w", "u"}: {}, {"u", "w"}: {}, // same articulation
	{"l", "n"}: {}, {"n", "l"}: {},
	{"m", "n"}: {}, {"n", "m"}: {}, // assimilation before labials
	{"ɹ", "ʊ"}: {}, {"ɹ", "ə"}: {}, {"ɹ", "ɚ"}: {}, // r-vocalisation
	{"d", "t"}: {}, {"t", "d"}: {}, // onset aspiration
	{"ŋ", "n"}: {}, {"n", "ŋ"}: {},
	{"ə", "ʌ"}: {}, {"ʌ", "ə"}: {}, // both are schwa-ish
}

// Word-final devoicing (lab -> lap) is a genuine and very common L1 error, but
// only in content words: in function words English itself reduces and devoices,
// which is what produced the "WAS" false positive.
var finalDevoicing = map[[2]string]struct{}{
	{"b", "p"}: {}, {"d", "t"}: {}, {"ɡ", "k"}: {}, {"g", "k"}: {},
	{"z", "s"}: {}, {"v", "f"}: {}, {"dʒ", "tʃ"}: {}, {"ð", "θ"}: {},
}

// Function words reduce and devoice in ordinary connected speech, so a
// devoiced ending here says nothing about the learner.
var pronFunctionWords = map[string]struct{}{
	"was": {}, "is": {}, "has": {}, "his": {}, "as": {}, "of": {}, "does": {},
	"had": {}, "and": {}, "would": {}, "could": {}, "should": {}, "did": {},
	"the": {}, "these": {}, "those": {}, "is'nt": {}, "'s": {}, "used": {},
	"said": {}, "goes": {}, "says": {}, "thatus": {}, "that": {}, "them": {},
	"there": {}, "their": {}, "with": {}, "to": {}, "do": {},
}

// Substitution mirrors one entry of the sidecar's "almashtirishlar".
type Substitution struct {
	Expected  string  `json:"kutilgan"`
	Heard     *string `json:"eshitilgan"` // nil when the sound was not heard at all
	WordFinal bool    `json:"oxirida"`
}

// ApplyConfidence rewrites each word's status according to the policy above,
// and reports how many verdicts were softened.
//
// It only ever downgrades: a word the sidecar accepted is never turned into an
// error. The aim is to stop telling learners they got something wrong when the
// evidence does not support it.
func ApplyConfidence(check *PronunciationCheck) (softened int) {
	if check == nil {
		return 0
	}
	for i := range check.Words {
		w := &check.Words[i]
		if w.Status != "xato" {
			continue
		}
		// "Bu so'z eshitilmadi" - no substitutions recorded. A word the model
		// did not hear at all is usually bad audio or a reading slip, not a
		// pronunciation error, so it becomes a hint too.
		if len(w.Substitutions) == 0 {
			w.Status = "kichik"
			softened++
			continue
		}
		if !hasReportableError(w.Word, w.Substitutions) {
			w.Status = "kichik"
			softened++
		}
	}
	return softened
}

// hasReportableError decides whether any one substitution in a word is solid
// enough to call an error.
func hasReportableError(word string, subs []Substitution) bool {
	clean := strings.ToLower(strings.Trim(word, ".,!?;:\"' "))
	_, isFunction := pronFunctionWords[clean]

	for _, s := range subs {
		if s.Heard == nil {
			// A dropped sound is only convincing for the sounds learners
			// actually drop, and only in a content word.
			if isFunction {
				continue
			}
			switch s.Expected {
			case "θ", "ð":
				return true
			}
			continue
		}
		pair := [2]string{s.Expected, *s.Heard}

		// Word-final devoicing is checked before the confusable list, and the
		// order matters: d->t sits in both. At the start of a word it is
		// aspiration and means nothing ("DO" heard as "to"), at the end of a
		// content word it is the classic "bad" -> "bat" error worth reporting.
		if s.WordFinal && !isFunction {
			if _, ok := finalDevoicing[pair]; ok {
				return true
			}
		}
		// Otherwise the confusable list wins: these turn up on correct speech.
		if _, bad := confusablePairs[pair]; bad {
			continue
		}
		if _, ok := highConfidenceErrors[pair]; ok {
			return true
		}
	}
	return false
}
