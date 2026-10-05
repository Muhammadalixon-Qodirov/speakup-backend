package services

import (
	"strings"
	"testing"
	"time"

	"github.com/speak-up/backend/internal/models"
)

// A centre's links become tappable targets on a card OTHER users see, so
// the sanitiser is a security boundary, not a convenience. These cases
// pin it down.
func TestSanitizeLinkRejectsDangerousSchemes(t *testing.T) {
	dangerous := []string{
		"javascript:alert(1)",
		"JavaScript:alert(1)",
		"  javascript:alert(1)  ",
		"data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==",
		"vbscript:msgbox(1)",
		"file:///etc/passwd",
		"ftp://example.com/x",
	}

	for _, in := range dangerous {
		if got, err := sanitizeLink(in); err == nil {
			t.Errorf("sanitizeLink(%q) accepted it as %q - must be refused", in, got)
		}
	}
}

func TestSanitizeLinkRejectsNonsense(t *testing.T) {
	bad := []string{
		"salom",          // no dot, not a host
		"http://",        // no host
		"://example.com", // no scheme
		strings.Repeat("a", 300) + ".com",
	}

	for _, in := range bad {
		if _, err := sanitizeLink(in); err == nil {
			t.Errorf("sanitizeLink(%q) should have been refused", in)
		}
	}
}

func TestSanitizeLinkAddsSchemeForBareHosts(t *testing.T) {
	// Centres type "instagram.com/ieltszone" far more often than a full
	// URL. Refusing that would be technically correct and practically
	// useless, so the sanitiser upgrades it instead.
	cases := map[string]string{
		"instagram.com/ieltszone":     "https://instagram.com/ieltszone",
		"t.me/ieltszone":              "https://t.me/ieltszone",
		"https://example.com/a":       "https://example.com/a",
		"http://example.com":          "http://example.com",
		"  instagram.com/ieltszone  ": "https://instagram.com/ieltszone",
	}

	for in, want := range cases {
		got, err := sanitizeLink(in)
		if err != nil {
			t.Errorf("sanitizeLink(%q) unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("sanitizeLink(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeCoursesDropsBlankRowsAndCaps(t *testing.T) {
	in := models.CourseList{
		{Title: "IELTS 6.5"},
		{Title: "   "},                  // a row the form left behind
		{Title: "", Description: "..."}, // ditto
		{Title: " General English ", Price: " 450 000 so'm "},
	}

	out, err := sanitizeCourses(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected blank rows to be dropped, got %d rows: %+v", len(out), out)
	}
	if out[1].Title != "General English" || out[1].Price != "450 000 so'm" {
		t.Fatalf("expected fields to be trimmed, got %+v", out[1])
	}

	// Over the cap must be refused, not silently truncated - a centre
	// that pasted 50 courses should be told, not have 38 vanish.
	tooMany := make(models.CourseList, maxCourses+1)
	for i := range tooMany {
		tooMany[i] = models.Course{Title: "Kurs"}
	}
	if _, err := sanitizeCourses(tooMany); err == nil {
		t.Fatal("expected an error when the course list exceeds the cap")
	}
}

// BannerEligible is what stands between a partner's logo and every user's
// screen. Each clause is asserted independently so removing one can't
// pass unnoticed.
func TestBannerEligibility(t *testing.T) {
	logo := "/uploads/centers/x.png"
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)

	base := func() *models.StudyCenter {
		return &models.StudyCenter{
			IsActive:         true,
			IsAdvertised:     true,
			ModerationStatus: models.CenterModerationApproved,
			LogoURL:          &logo,
		}
	}

	if !base().BannerEligible() {
		t.Fatal("a fully approved, in-contract, advertised centre must be eligible")
	}

	cases := []struct {
		name   string
		mutate func(*models.StudyCenter)
	}{
		{"inactive centre", func(c *models.StudyCenter) { c.IsActive = false }},
		{"advertising not in contract", func(c *models.StudyCenter) { c.IsAdvertised = false }},
		{"contract expired", func(c *models.StudyCenter) { c.ContractExpiresAt = &past }},
		{"pending moderation", func(c *models.StudyCenter) { c.ModerationStatus = models.CenterModerationPending }},
		{"rejected", func(c *models.StudyCenter) { c.ModerationStatus = models.CenterModerationRejected }},
		{"draft", func(c *models.StudyCenter) { c.ModerationStatus = models.CenterModerationDraft }},
		{"no logo", func(c *models.StudyCenter) { c.LogoURL = nil }},
		{"empty logo", func(c *models.StudyCenter) { empty := ""; c.LogoURL = &empty }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			tc.mutate(c)
			if c.BannerEligible() {
				t.Fatalf("%s must NOT be banner-eligible", tc.name)
			}
		})
	}

	// A future expiry is still a live contract.
	c := base()
	c.ContractExpiresAt = &future
	if !c.BannerEligible() {
		t.Fatal("a contract expiring in the future is still live")
	}
}

// Editing a phone number must not black out a partner's banner; editing
// what the banner actually says must.
func TestReviewableChangeSplit(t *testing.T) {
	about := "IELTS markazi"
	center := &models.StudyCenter{
		Name:  "IELTS Zone",
		About: &about,
	}

	str := func(s string) *string { return &s }

	if reviewableChange(center, CenterProfileInput{Phone: str("+998901234567")}) {
		t.Error("a phone-number edit must not require re-review")
	}
	if reviewableChange(center, CenterProfileInput{Address: str("Chilonzor 5")}) {
		t.Error("an address edit must not require re-review")
	}
	if reviewableChange(center, CenterProfileInput{InstagramURL: str("instagram.com/x")}) {
		t.Error("a social-link edit must not require re-review")
	}

	if !reviewableChange(center, CenterProfileInput{Name: str("Boshqa Markaz")}) {
		t.Error("renaming the centre must require re-review")
	}
	if !reviewableChange(center, CenterProfileInput{About: str("Butunlay boshqa matn")}) {
		t.Error("rewriting the description must require re-review")
	}
	if !reviewableChange(center, CenterProfileInput{Courses: &models.CourseList{{Title: "Yangi"}}}) {
		t.Error("changing the course list must require re-review")
	}

	// Submitting the SAME values is not a change and must not knock a
	// live banner offline.
	if reviewableChange(center, CenterProfileInput{Name: str("IELTS Zone")}) {
		t.Error("re-submitting the identical name is not a change")
	}
	if reviewableChange(center, CenterProfileInput{About: str("IELTS markazi")}) {
		t.Error("re-submitting the identical description is not a change")
	}
}
