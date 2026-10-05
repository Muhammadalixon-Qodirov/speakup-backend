package services

import (
	"encoding/json"
	"testing"
)

// coerceAnalysisJSON exists because the LLM does not reliably honour the
// shape it is asked for. Every case below is a shape that has actually
// been seen in production, or is one step away from one - and each of
// them, unhandled, costs a real user their entire speaking test.
func TestCoerceAnalysisJSONShapes(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{
			name: "well formed",
			in: `{"fluency_score":6,"grammar_errors":["a","b"],
			      "suggestions":["s"],"fluency_feedback":"good"}`,
		},
		{
			name: "string where a list is expected",
			in:   `{"grammar_errors":"only one error","suggestions":"only one tip"}`,
		},
		{
			name: "nested list inside the list",
			// This is the shape that survived the first fix and kept
			// failing in production: Go reports it as "cannot unmarshal
			// array into ... of type string".
			in: `{"grammar_errors":[["past tense","wrong"],"second"],"suggestions":[["a","b"]]}`,
		},
		{
			name: "objects inside the list",
			in:   `{"grammar_errors":[{"error":"x","fix":"y"}],"suggestions":[{"tip":"z"}]}`,
		},
		{
			name: "numbers inside the list",
			in:   `{"grammar_errors":[1,2,3],"suggestions":[4]}`,
		},
		{
			name: "list where a string is expected",
			in:   `{"fluency_feedback":["one","two"],"grammar_feedback":["x"]}`,
		},
		{
			name: "list where a score is expected",
			in:   `{"fluency_score":[6.5,7],"overall_band":[6]}`,
		},
		{
			name: "everything wrong at once",
			in: `{"fluency_score":[6],"fluency_feedback":["a","b"],
			      "grammar_errors":[["x"],{"y":1},"z"],"suggestions":"one"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixed := coerceAnalysisJSON(tc.in)

			var analysis IELTSAnalysis
			if err := json.Unmarshal([]byte(fixed), &analysis); err != nil {
				t.Fatalf("coerced JSON still fails to parse: %v\ninput:   %s\ncoerced: %s",
					err, tc.in, fixed)
			}
		})
	}
}

// Malformed input must be returned untouched rather than turned into
// something that parses into nonsense - the caller's error is the honest
// outcome there.
func TestCoerceAnalysisJSONPassesThroughGarbage(t *testing.T) {
	garbage := `not json at all`
	if got := coerceAnalysisJSON(garbage); got != garbage {
		t.Fatalf("expected unparseable input to pass through unchanged, got %q", got)
	}
}

// Coercion must not invent or lose content.
func TestCoerceAnalysisJSONPreservesValues(t *testing.T) {
	in := `{"grammar_errors":[["past","tense"],"second"],"suggestions":"only one"}`

	var analysis IELTSAnalysis
	if err := json.Unmarshal([]byte(coerceAnalysisJSON(in)), &analysis); err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if len(analysis.GrammarErrors) != 2 {
		t.Fatalf("expected 2 grammar errors, got %d: %v", len(analysis.GrammarErrors), analysis.GrammarErrors)
	}
	if analysis.GrammarErrors[0] != "past tense" {
		t.Errorf("nested list should join into one line, got %q", analysis.GrammarErrors[0])
	}
	if analysis.GrammarErrors[1] != "second" {
		t.Errorf("plain string element should survive, got %q", analysis.GrammarErrors[1])
	}
	if len(analysis.Suggestions) != 1 || analysis.Suggestions[0] != "only one" {
		t.Errorf("bare string should become a one-element list, got %v", analysis.Suggestions)
	}
}
