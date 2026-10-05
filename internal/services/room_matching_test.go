package services

import (
	"fmt"
	"strings"
	"testing"

	"github.com/speak-up/backend/internal/models"
)

// These tests cover the pure logic of room matching - the pairing
// algorithm and the repeat-avoidance scoring. Anything that touches
// Redis or Postgres is deliberately left out; those paths are exercised
// against the real services on staging.

func TestRepeatPenaltyOrdering(t *testing.T) {
	recent := []string{"most", "middle", "oldest"}

	fresh := repeatPenalty(recent, "someone-new")
	if fresh != 0 {
		t.Fatalf("a never-seen partner must carry no penalty, got %v", fresh)
	}

	pMost := repeatPenalty(recent, "most")
	pMiddle := repeatPenalty(recent, "middle")
	pOldest := repeatPenalty(recent, "oldest")

	if !(pMost > pMiddle && pMiddle > pOldest) {
		t.Fatalf("penalty must decay with age: most=%v middle=%v oldest=%v", pMost, pMiddle, pOldest)
	}
	if pOldest <= 0 {
		t.Fatalf("every remembered partner must carry SOME penalty, oldest=%v", pOldest)
	}

	// The whole design rests on this: the smallest repeat penalty must
	// still outweigh the largest possible positive score (random jitter
	// 100 + wait bonus 60), otherwise a repeat could beat a fresh partner.
	const maxPositiveScore = 100 + 60
	if pOldest <= maxPositiveScore {
		t.Fatalf("smallest repeat penalty (%v) must exceed max positive score (%d)", pOldest, maxPositiveScore)
	}
}

func TestPairGreedyAvoidsRecentPartners(t *testing.T) {
	// A and B just spoke. With C and D also waiting, neither A nor B
	// should be handed back to the other.
	order := []string{"A", "B", "C", "D"}
	recent := map[string][]string{
		"A": {"B"},
		"B": {"A"},
	}

	pairs, leftover := pairGreedy(order, func(uid string) []string { return recent[uid] })

	if len(pairs) != 2 {
		t.Fatalf("expected 2 pairs from 4 students, got %d (%v)", len(pairs), pairs)
	}
	if len(leftover) != 0 {
		t.Fatalf("expected nobody left over, got %v", leftover)
	}
	for _, p := range pairs {
		if (p[0] == "A" && p[1] == "B") || (p[0] == "B" && p[1] == "A") {
			t.Fatalf("A and B were re-paired despite being recent partners: %v", pairs)
		}
	}
	assertNoDuplicates(t, pairs)
}

func TestPairGreedyFallsBackWhenEveryoneIsARepeat(t *testing.T) {
	// Two students, and they have already spoken. Refusing to match them
	// would strand the pair; the fallback must pair them anyway.
	order := []string{"A", "B"}
	recent := map[string][]string{
		"A": {"B"},
		"B": {"A"},
	}

	pairs, leftover := pairGreedy(order, func(uid string) []string { return recent[uid] })

	if len(pairs) != 1 {
		t.Fatalf("expected the pair to be made anyway, got %d pairs", len(pairs))
	}
	if len(leftover) != 0 {
		t.Fatalf("expected nobody left over, got %v", leftover)
	}
}

func TestPairGreedyPrefersLeastRecentFallback(t *testing.T) {
	// A has spoken to everyone. Their recent list is newest-first, so
	// "C" (oldest) should be preferred over "B" (most recent).
	order := []string{"A", "B", "C"}
	recent := map[string][]string{
		"A": {"B", "C"},
	}

	pairs, _ := pairGreedy(order, func(uid string) []string { return recent[uid] })

	if len(pairs) != 1 {
		t.Fatalf("expected 1 pair from 3 students, got %d", len(pairs))
	}
	if pairs[0][0] != "A" || pairs[0][1] != "C" {
		t.Fatalf("expected A to fall back to their oldest partner C, got %v", pairs[0])
	}
}

func TestPairGreedyOddCountLeavesExactlyOne(t *testing.T) {
	order := []string{"A", "B", "C", "D", "E"}

	pairs, leftover := pairGreedy(order, func(string) []string { return nil })

	if len(pairs) != 2 {
		t.Fatalf("expected 2 pairs from 5 students, got %d", len(pairs))
	}
	if len(leftover) != 1 {
		t.Fatalf("expected exactly 1 student left over, got %v", leftover)
	}
	assertNoDuplicates(t, pairs)
}

func TestPairGreedyHandlesTinyInputs(t *testing.T) {
	if pairs, leftover := pairGreedy(nil, func(string) []string { return nil }); len(pairs) != 0 || len(leftover) != 0 {
		t.Fatalf("empty input must produce nothing, got pairs=%v leftover=%v", pairs, leftover)
	}
	pairs, leftover := pairGreedy([]string{"solo"}, func(string) []string { return nil })
	if len(pairs) != 0 {
		t.Fatalf("a single student cannot be paired, got %v", pairs)
	}
	if len(leftover) != 1 || leftover[0] != "solo" {
		t.Fatalf("the lone student must be returned as leftover, got %v", leftover)
	}
}

func TestPairGreedyLargeClassPairsEveryone(t *testing.T) {
	// 20 students, nobody has spoken yet - everyone must be paired and
	// nobody may appear twice.
	order := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		order = append(order, fmt.Sprintf("u%02d", i))
	}

	pairs, leftover := pairGreedy(order, func(string) []string { return nil })

	if len(pairs) != 10 {
		t.Fatalf("expected 10 pairs from 20 students, got %d", len(pairs))
	}
	if len(leftover) != 0 {
		t.Fatalf("expected nobody left over, got %v", leftover)
	}
	assertNoDuplicates(t, pairs)
}

// assertNoDuplicates is the invariant that matters most: a student must
// never end up in two simultaneous sessions.
func assertNoDuplicates(t *testing.T, pairs [][2]string) {
	t.Helper()
	seen := map[string]bool{}
	for _, p := range pairs {
		for _, uid := range p {
			if seen[uid] {
				t.Fatalf("student %q appears in more than one pair: %v", uid, pairs)
			}
			seen[uid] = true
		}
	}
}

func TestNormalizeRoomCode(t *testing.T) {
	cases := map[string]string{
		"ABC12345":      "ABC12345",
		"abc12345":      "ABC12345",
		" abc12345 ":    "ABC12345",
		"room_ABC12345": "ABC12345",
		"ROOM_abc12345": "ABC12345",
		"room-ABC12345": "ABC12345",
		"ABC-123-45":    "ABC12345",
		"":              "",
		"room_":         "",
		"!!!":           "",
	}
	for in, want := range cases {
		if got := NormalizeRoomCode(in); got != want {
			t.Errorf("NormalizeRoomCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGenerateRoomCodeShape(t *testing.T) {
	const alphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		code := models.GenerateRoomCode()
		if len(code) != 8 {
			t.Fatalf("code %q has length %d, want 8", code, len(code))
		}
		for _, r := range code {
			if !strings.ContainsRune(alphabet, r) {
				t.Fatalf("code %q contains %q, which is outside the unambiguous alphabet", code, r)
			}
		}
		// Round-tripping through the normaliser must be lossless,
		// otherwise a freshly minted code wouldn't resolve on join.
		if NormalizeRoomCode(code) != code {
			t.Fatalf("code %q does not survive NormalizeRoomCode", code)
		}
		seen[code] = true
	}

	// 500 draws from a ~3.8e11 space: any meaningful collision rate here
	// means the generator is broken, not unlucky.
	if len(seen) < 495 {
		t.Fatalf("only %d unique codes out of 500 - generator lacks entropy", len(seen))
	}
}
