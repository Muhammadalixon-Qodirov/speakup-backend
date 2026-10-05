package services

// Shared scoring helpers for the public leaderboard AND the Monday-cron
// prize job. Originally lived in the handlers package; hoisted here so
// services can reuse them without a handlers→services→handlers cycle.

// Bayesian average constants. m controls how strongly low-vote users
// are pulled toward the prior; with our scale (top users at 50-100
// ratings), m=20 gives high-volume users a clear advantage over
// newcomers with a handful of perfect scores while still letting an
// objectively excellent user climb after they've earned ~20+ ratings.
//
// Reference: IMDB Top 250 uses m=25,000 (millions of votes), Yelp
// uses ~10 plus time decay. Industry consensus: m ≈ ¼ to ½ of typical
// "established user" vote count.
const (
	bayesianMinVotes  = 20  // m: votes that compete with the prior
	bayesianGlobalAvg = 4.0 // C: prior - what we assume the average user is
)

// BayesianRating returns a weighted average that protects against rating
// inflation from sparse samples. A user with 1 × 5★ rating won't outrank
// a user with 10 × 4.5★ ratings.
//
//	weighted = (v / (v + m)) * R + (m / (v + m)) * C
//
// where R = the user's raw average, v = number of ratings, m = threshold,
// C = global average prior.
func BayesianRating(rawAvg float64, votes int) float64 {
	v := float64(votes)
	m := float64(bayesianMinVotes)
	if v+m == 0 {
		return bayesianGlobalAvg
	}
	return (v/(v+m))*rawAvg + (m/(v+m))*bayesianGlobalAvg
}

// Composite returns the ranking score for a leaderboard row.
//
//	streak          × 10   max ~30 → 300
//	weightedRating  × 20   max 5.0 → 100   (Bayesian-shrunk)
//	minutes         ÷  3   1000m   → 333
//	referrals       ×  5   max 20  → 100
//
// Higher = better.
func Composite(streak int, weightedRating float64, minutes int, referrals int) float64 {
	return float64(streak)*10 +
		weightedRating*20 +
		float64(minutes)/3 +
		float64(referrals)*5
}
