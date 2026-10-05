package ws

import "testing"

// The listen-mode relay is the only place where a THIRD party is allowed
// to push WebRTC signalling into someone else's live call. Its
// authorisation rule is therefore worth pinning down exhaustively: every
// one of the 16 combinations is asserted below, so nobody can loosen the
// gate by accident.
func TestMayRelayListenSignal(t *testing.T) {
	type roles struct {
		senderListener bool
		senderSpeaker  bool
		targetListener bool
		targetSpeaker  bool
	}

	cases := []struct {
		name string
		r    roles
		want bool
	}{
		// The two legitimate directions.
		{
			name: "teacher to student",
			r:    roles{senderListener: true, targetSpeaker: true},
			want: true,
		},
		{
			name: "student to teacher",
			r:    roles{senderSpeaker: true, targetListener: true},
			want: true,
		},

		// Speaker-to-speaker belongs on the normal signalling path.
		{
			name: "student to student is refused",
			r:    roles{senderSpeaker: true, targetSpeaker: true},
			want: false,
		},
		// Two teachers monitoring the same call must not negotiate media
		// with each other through it.
		{
			name: "teacher to teacher is refused",
			r:    roles{senderListener: true, targetListener: true},
			want: false,
		},

		// Anyone not attached to the session at all.
		{
			name: "stranger to student is refused",
			r:    roles{targetSpeaker: true},
			want: false,
		},
		{
			name: "student to stranger is refused",
			r:    roles{senderSpeaker: true},
			want: false,
		},
		{
			name: "stranger to teacher is refused",
			r:    roles{targetListener: true},
			want: false,
		},
		{
			name: "teacher to stranger is refused",
			r:    roles{senderListener: true},
			want: false,
		},
		{
			name: "stranger to stranger is refused",
			r:    roles{},
			want: false,
		},

		// Defence in depth: a user who is somehow BOTH is never trusted,
		// in either position.
		{
			name: "sender who is both roles is refused",
			r:    roles{senderListener: true, senderSpeaker: true, targetSpeaker: true},
			want: false,
		},
		{
			name: "target who is both roles is refused",
			r:    roles{senderSpeaker: true, targetListener: true, targetSpeaker: true},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mayRelayListenSignal(
				tc.r.senderListener, tc.r.senderSpeaker,
				tc.r.targetListener, tc.r.targetSpeaker,
			)
			if got != tc.want {
				t.Fatalf("mayRelayListenSignal(%+v) = %v, want %v", tc.r, got, tc.want)
			}
		})
	}
}

// A relay that permits everything would pass every "want: true" case
// above, so assert the opposite direction too: the rule must refuse the
// clear majority of role combinations.
func TestMayRelayListenSignalIsRestrictiveByDefault(t *testing.T) {
	allowed := 0
	total := 0

	for i := 0; i < 16; i++ {
		sl := i&1 != 0
		ss := i&2 != 0
		tl := i&4 != 0
		ts := i&8 != 0
		total++
		if mayRelayListenSignal(sl, ss, tl, ts) {
			allowed++
		}
	}

	// Exactly two of the sixteen combinations are legal:
	// (listener → speaker) and (speaker → listener), with neither side
	// holding both roles.
	if allowed != 2 {
		t.Fatalf("expected exactly 2 of %d role combinations to be relayable, got %d", total, allowed)
	}
}
