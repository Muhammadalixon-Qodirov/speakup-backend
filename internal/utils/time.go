package utils

import "time"

// tashkent returns the Asia/Tashkent location or UTC if the tzdata is
// missing in the runtime image. Slim Docker bases sometimes ship without
// zoneinfo - UTC+5 is the correct fallback for Uzbekistan and keeps the
// week boundary within ~0 minutes of the real wall clock.
func tashkent() *time.Location {
	loc, err := time.LoadLocation("Asia/Tashkent")
	if err != nil || loc == nil {
		return time.FixedZone("UZT", 5*3600)
	}
	return loc
}

// StartOfTashkentWeek returns Monday 00:00:00 of the Tashkent-local
// week containing t. "Week" here is the ISO / Uzbek convention -
// Monday is the first day, Sunday the last. Example:
//
//	t = Fri 2026-04-17 14:23 UZT → Mon 2026-04-13 00:00 UZT
//	t = Sun 2026-04-19 23:59 UZT → Mon 2026-04-13 00:00 UZT
//	t = Mon 2026-04-20 00:00 UZT → Mon 2026-04-20 00:00 UZT
func StartOfTashkentWeek(t time.Time) time.Time {
	loc := tashkent()
	local := t.In(loc)

	// time.Weekday: Sunday=0, Monday=1, ..., Saturday=6.
	// We want Monday to be "day 0" of the week.
	offset := int(local.Weekday()) - 1
	if offset < 0 {
		offset = 6 // Sunday is 6 days after Monday
	}

	midnight := time.Date(
		local.Year(), local.Month(), local.Day(),
		0, 0, 0, 0, loc,
	)
	return midnight.AddDate(0, 0, -offset)
}

// NextTashkentWeekStart returns the next Monday 00:00 Tashkent strictly
// after t. Used by the frontend countdown ("keyingi reset: X soat").
func NextTashkentWeekStart(t time.Time) time.Time {
	return StartOfTashkentWeek(t).AddDate(0, 0, 7)
}
