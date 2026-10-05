package services

import (
	"context"
	"errors"
	"math/rand"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
	"gorm.io/gorm"
)

var (
	ErrCenterNotFound = errors.New("study center not found")
	ErrNotCenterOwner = errors.New("not the owner of this study center")
)

// --- Creation (platform admins only) ---

// CreateCenter registers a partner learning centre and assigns its admin.
//
// Called exclusively from the admin API. A centre that could sign itself
// up would be a free advertising slot for anyone who asked, which is
// exactly what section 4.6 of the spec forbids.
func CreateCenter(ownerID uuid.UUID, name string, contractExpiresAt *time.Time) (*models.StudyCenter, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("center name is required")
	}

	var owner models.User
	if err := database.DB.First(&owner, "id = ?", ownerID).Error; err != nil {
		return nil, errors.New("owner user not found")
	}
	if owner.IsBanned {
		return nil, errors.New("owner is banned")
	}

	center := models.StudyCenter{
		OwnerID:           ownerID,
		Name:              name,
		IsActive:          true,
		IsAdvertised:      false, // advertising is a separate contract term
		ContractExpiresAt: contractExpiresAt,
		ModerationStatus:  models.CenterModerationDraft,
	}
	if err := database.DB.Create(&center).Error; err != nil {
		return nil, err
	}

	center.Owner = &owner
	return &center, nil
}

// --- Lookups ---

func GetCenterByID(centerID uuid.UUID) (*models.StudyCenter, error) {
	var c models.StudyCenter
	err := database.DB.Preload("Owner").First(&c, "id = ?", centerID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrCenterNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// GetCenterByOwner returns the centre this user administers, if any.
func GetCenterByOwner(ownerID uuid.UUID) (*models.StudyCenter, error) {
	var c models.StudyCenter
	err := database.DB.Preload("Owner").
		Where("owner_id = ?", ownerID).
		Order("created_at ASC").
		First(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrCenterNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// IsCenterOwner is the cheap gate used by the handlers.
func IsCenterOwner(centerID, userID uuid.UUID) bool {
	var count int64
	database.DB.Model(&models.StudyCenter{}).
		Where("id = ? AND owner_id = ?", centerID, userID).
		Count(&count)
	return count > 0
}

// --- Profile editing (the centre itself) ---

// CenterProfileInput is the editable half of a centre profile. Nil fields
// are left untouched, so the frontend can PATCH one field at a time.
type CenterProfileInput struct {
	Name         *string
	About        *string
	Courses      *models.CourseList
	Phone        *string
	Address      *string
	TelegramURL  *string
	InstagramURL *string
	WebsiteURL   *string
	SignupURL    *string
}

// reviewableChange reports whether an edit touches content that is shown
// to OTHER users as advertising and therefore has to be re-approved.
//
// The split is deliberate and narrow:
//
//	re-review:  name, about, courses, logo  - brand claims, prices,
//	            free text. This is where impersonation and misleading
//	            offers live.
//	go live:    phone, address, social links - low-risk contact details,
//	            format-validated, and self-correcting (a wrong phone
//	            number hurts only the centre that typed it).
//
// Without this split a centre fixing a typo in its phone number would
// black out its own banner until an admin happened to log in, which is a
// good way to make partners stop updating anything at all.
func reviewableChange(c *models.StudyCenter, in CenterProfileInput) bool {
	if in.Name != nil && strings.TrimSpace(*in.Name) != c.Name {
		return true
	}
	if in.About != nil && !sameOptionalString(in.About, c.About) {
		return true
	}
	if in.Courses != nil {
		return true
	}
	return false
}

func sameOptionalString(in *string, current *string) bool {
	v := strings.TrimSpace(*in)
	if current == nil {
		return v == ""
	}
	return v == *current
}

// UpdateCenterProfile applies a partial profile edit.
//
// Returns whether the change pushed the centre back into moderation, so
// the handler can tell the owner "banner tasdiqlanguncha ko'rinmaydi".
func UpdateCenterProfile(centerID uuid.UUID, in CenterProfileInput) (*models.StudyCenter, bool, error) {
	center, err := GetCenterByID(centerID)
	if err != nil {
		return nil, false, err
	}

	updates := map[string]interface{}{}

	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return nil, false, errors.New("markaz nomi bo'sh bo'lishi mumkin emas")
		}
		if len([]rune(name)) > 128 {
			return nil, false, errors.New("markaz nomi juda uzun")
		}
		updates["name"] = name
	}
	if in.About != nil {
		about := strings.TrimSpace(*in.About)
		if len([]rune(about)) > 1024 {
			return nil, false, errors.New("tavsif juda uzun")
		}
		updates["about"] = nullableString(about)
	}
	if in.Courses != nil {
		courses, err := sanitizeCourses(*in.Courses)
		if err != nil {
			return nil, false, err
		}
		updates["courses"] = courses
	}
	if in.Phone != nil {
		phone := strings.TrimSpace(*in.Phone)
		if len([]rune(phone)) > 32 {
			return nil, false, errors.New("telefon raqami juda uzun")
		}
		updates["phone"] = nullableString(phone)
	}
	if in.Address != nil {
		addr := strings.TrimSpace(*in.Address)
		if len([]rune(addr)) > 256 {
			return nil, false, errors.New("manzil juda uzun")
		}
		updates["address"] = nullableString(addr)
	}

	// Link fields are validated, not just length-capped: they become
	// tappable targets on a card other people see, so a javascript: or
	// data: URL there would be a stored-XSS vector on the client.
	linkFields := map[string]*string{
		"telegram_url":  in.TelegramURL,
		"instagram_url": in.InstagramURL,
		"website_url":   in.WebsiteURL,
		"signup_url":    in.SignupURL,
	}
	for column, value := range linkFields {
		if value == nil {
			continue
		}
		raw := strings.TrimSpace(*value)
		if raw == "" {
			updates[column] = nil
			continue
		}
		clean, err := sanitizeLink(raw)
		if err != nil {
			return nil, false, err
		}
		updates[column] = clean
	}

	// Only an APPROVED profile is knocked back into the queue by an edit.
	//
	// A draft or rejected centre stays where it is until the owner
	// explicitly submits - otherwise typing a name into an empty profile
	// would drop a half-finished centre (no logo, no contacts) into the
	// admin's moderation queue, and the admin would have nothing to
	// approve or reject.
	needsReview := reviewableChange(center, in) &&
		center.ModerationStatus == models.CenterModerationApproved
	if needsReview {
		updates["moderation_status"] = models.CenterModerationPending
		updates["moderation_note"] = nil
	}

	if len(updates) == 0 {
		return center, false, nil
	}

	if err := database.DB.Model(&models.StudyCenter{}).
		Where("id = ?", centerID).
		Updates(updates).Error; err != nil {
		return nil, false, err
	}

	updated, err := GetCenterByID(centerID)
	return updated, needsReview, err
}

// sanitizeLink accepts only http(s) URLs. Anything else - javascript:,
// data:, a bare word - is refused rather than silently stored.
//
// It trims its own input. Callers happen to trim too, but this is a
// security boundary and must not depend on being called correctly: with
// leading whitespace the "://" test below would miss, and the value would
// take a different path through the function than the caller intended.
func sanitizeLink(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("havola bo'sh")
	}
	if len([]rune(raw)) > 256 {
		return "", errors.New("havola juda uzun")
	}

	// Refuse a non-http scheme UP FRONT rather than hoping url.Parse
	// makes it look malformed later. "javascript:alert(1)" contains no
	// "://", so without this check it would get an "https://" glued onto
	// the front and be judged on whatever that parsed into - which is
	// the kind of reasoning that eventually lets one through.
	if i := strings.Index(raw, ":"); i > 0 {
		scheme := strings.ToLower(raw[:i])
		// A colon that appears after a slash or inside a hostname is not
		// a scheme (e.g. "example.com:8080/x", "site.com/a:b").
		if !strings.ContainsAny(scheme, "/.") {
			if scheme != "http" && scheme != "https" {
				return "", errors.New("havola faqat http yoki https bo'lishi mumkin")
			}
		}
	}

	// A centre will type "instagram.com/ieltszone" far more often than a
	// full URL, so add the scheme rather than rejecting them.
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("havola noto'g'ri")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("havola faqat http yoki https bo'lishi mumkin")
	}
	if u.Host == "" || !strings.Contains(u.Host, ".") {
		return "", errors.New("havola noto'g'ri")
	}
	// Whitespace inside a host is never legitimate and is a classic way
	// to smuggle something past a naive parser.
	if strings.ContainsAny(u.Host, " \t\n\r") {
		return "", errors.New("havola noto'g'ri")
	}
	return u.String(), nil
}

// maxCourses caps the list so a profile card stays a card.
const maxCourses = 12

func sanitizeCourses(in models.CourseList) (models.CourseList, error) {
	if len(in) > maxCourses {
		return nil, errors.New("kurslar soni juda ko'p")
	}
	out := make(models.CourseList, 0, len(in))
	for _, c := range in {
		title := strings.TrimSpace(c.Title)
		if title == "" {
			continue // silently drop blank rows the form left behind
		}
		if len([]rune(title)) > 128 {
			return nil, errors.New("kurs nomi juda uzun")
		}
		desc := strings.TrimSpace(c.Description)
		if len([]rune(desc)) > 512 {
			return nil, errors.New("kurs tavsifi juda uzun")
		}
		dur := strings.TrimSpace(c.Duration)
		price := strings.TrimSpace(c.Price)
		if len([]rune(dur)) > 64 || len([]rune(price)) > 64 {
			return nil, errors.New("kurs maydoni juda uzun")
		}
		out = append(out, models.Course{
			Title:       title,
			Description: desc,
			Duration:    dur,
			Price:       price,
		})
	}
	return out, nil
}

func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// SubmitCenterForReview moves a draft/rejected profile into the queue.
//
// The completeness check lives here rather than in the admin's head: a
// banner needs a logo, and a profile card with no way to contact the
// centre is worse than no card at all.
func SubmitCenterForReview(centerID uuid.UUID) (*models.StudyCenter, error) {
	center, err := GetCenterByID(centerID)
	if err != nil {
		return nil, err
	}

	if center.LogoURL == nil || *center.LogoURL == "" {
		return nil, errors.New("logotip yuklanmagan")
	}
	hasContact := (center.Phone != nil && *center.Phone != "") ||
		(center.TelegramURL != nil && *center.TelegramURL != "") ||
		(center.InstagramURL != nil && *center.InstagramURL != "") ||
		(center.WebsiteURL != nil && *center.WebsiteURL != "")
	if !hasContact {
		return nil, errors.New("kamida bitta aloqa ma'lumoti kerak")
	}

	if err := database.DB.Model(&models.StudyCenter{}).
		Where("id = ?", centerID).
		Updates(map[string]interface{}{
			"moderation_status": models.CenterModerationPending,
			"moderation_note":   nil,
		}).Error; err != nil {
		return nil, err
	}

	return GetCenterByID(centerID)
}

// MarkCenterPending is what the logo upload calls: new artwork is new
// content, so it goes back through review.
func MarkCenterPending(centerID uuid.UUID) {
	database.DB.Model(&models.StudyCenter{}).
		Where("id = ? AND moderation_status = ?", centerID, models.CenterModerationApproved).
		Update("moderation_status", models.CenterModerationPending)
}

// --- Moderation (platform admin) ---

// ModerateCenter approves or rejects a profile. `note` is shown to the
// centre, so a rejection should say what to fix.
func ModerateCenter(centerID uuid.UUID, reviewerID uuid.UUID, approve bool, note string) (*models.StudyCenter, error) {
	status := models.CenterModerationRejected
	if approve {
		status = models.CenterModerationApproved
	}

	now := time.Now()
	updates := map[string]interface{}{
		"moderation_status": status,
		"reviewed_at":       now,
		"reviewed_by":       reviewerID,
		"moderation_note":   nullableString(strings.TrimSpace(note)),
	}

	if err := database.DB.Model(&models.StudyCenter{}).
		Where("id = ?", centerID).
		Updates(updates).Error; err != nil {
		return nil, err
	}
	return GetCenterByID(centerID)
}

// SetCenterContract updates the commercial terms. Admin only.
func SetCenterContract(centerID uuid.UUID, isActive, isAdvertised *bool, expiresInDays *int) (*models.StudyCenter, error) {
	updates := map[string]interface{}{}
	if isActive != nil {
		updates["is_active"] = *isActive
	}
	if isAdvertised != nil {
		updates["is_advertised"] = *isAdvertised
	}
	if expiresInDays != nil {
		if *expiresInDays <= 0 {
			updates["contract_expires_at"] = nil
		} else {
			updates["contract_expires_at"] = time.Now().AddDate(0, 0, *expiresInDays)
		}
	}
	if len(updates) == 0 {
		return GetCenterByID(centerID)
	}

	if err := database.DB.Model(&models.StudyCenter{}).
		Where("id = ?", centerID).
		Updates(updates).Error; err != nil {
		return nil, err
	}
	return GetCenterByID(centerID)
}

// --- Banner rotation ---

// bannerRotationSize caps how many centres a single client is given to
// rotate through. Small enough that each partner gets meaningful screen
// time in one session; large enough that the rotation doesn't feel like
// the same logo over and over.
const bannerRotationSize = 8

// CenterBanner is the slim payload the in-session banner renders.
// Deliberately minimal - the full profile is fetched only if the user
// actually taps it.
type CenterBanner struct {
	ID      uuid.UUID `json:"id"`
	Name    string    `json:"name"`
	LogoURL string    `json:"logo_url"`
	Tagline *string   `json:"tagline"`
}

// SessionBanners returns the centres to rotate through in a live session.
//
// Fairness: eligible centres are ordered by how few impressions they have
// had, then the head of that list is shuffled. Ordering purely at random
// would let luck starve a small partner; ordering purely by impressions
// would make the rotation deterministic and predictable. Least-shown-first
// with a shuffled head gives every partner comparable exposure without
// showing them in a fixed sequence.
func SessionBanners() []CenterBanner {
	var centers []models.StudyCenter
	err := database.DB.
		Where("is_active = ? AND is_advertised = ? AND moderation_status = ?",
			true, true, models.CenterModerationApproved).
		Where("logo_url IS NOT NULL AND logo_url != ''").
		Where("contract_expires_at IS NULL OR contract_expires_at > ?", time.Now()).
		Order("banner_impressions ASC").
		Limit(bannerRotationSize * 2).
		Find(&centers).Error
	if err != nil || len(centers) == 0 {
		return nil
	}

	if len(centers) > bannerRotationSize {
		centers = centers[:bannerRotationSize]
	}
	rand.Shuffle(len(centers), func(i, j int) { centers[i], centers[j] = centers[j], centers[i] })

	out := make([]CenterBanner, 0, len(centers))
	ids := make([]uuid.UUID, 0, len(centers))
	for _, c := range centers {
		logo := ""
		if c.LogoURL != nil {
			logo = *c.LogoURL
		}
		out = append(out, CenterBanner{
			ID:      c.ID,
			Name:    c.Name,
			LogoURL: logo,
			Tagline: c.About,
		})
		ids = append(ids, c.ID)
	}

	// One impression per centre per served rotation. This is the number
	// partners are quoted, so it is counted server-side where a client
	// can't inflate it.
	go recordBannerImpressions(ids)

	return out
}

func recordBannerImpressions(ids []uuid.UUID) {
	defer func() {
		if r := recover(); r != nil {
			log.Warn().Interface("panic", r).Msg("recordBannerImpressions recovered")
		}
	}()
	if len(ids) == 0 {
		return
	}
	database.DB.Model(&models.StudyCenter{}).
		Where("id IN ?", ids).
		UpdateColumn("banner_impressions", gorm.Expr("banner_impressions + 1"))
}

// bannerClickCooldown stops one user's repeated taps from inflating a
// partner's click count. Per (user, centre), one counted click per hour.
const bannerClickCooldown = time.Hour

// RecordBannerClick increments a centre's click counter, deduplicated per
// user. Returns false when the click was a repeat inside the cooldown.
func RecordBannerClick(centerID uuid.UUID, userID string) bool {
	ctx := context.Background()
	key := "banner_click:" + centerID.String() + ":" + userID

	first, err := database.Redis.SetNX(ctx, key, "1", bannerClickCooldown).Result()
	if err != nil {
		// Redis down - count it rather than lose it. Over-counting on an
		// outage is better than telling a partner they got zero clicks.
		first = true
	}
	if !first {
		return false
	}

	database.DB.Model(&models.StudyCenter{}).
		Where("id = ?", centerID).
		UpdateColumn("banner_clicks", gorm.Expr("banner_clicks + 1"))
	return true
}

// PublicCenterProfile is what a user sees after tapping a banner.
//
// Only approved, in-contract centres resolve - otherwise a stale link
// shared in a chat would keep opening the profile of a partner we no
// longer work with.
func PublicCenterProfile(centerID uuid.UUID) (*models.StudyCenter, error) {
	center, err := GetCenterByID(centerID)
	if err != nil {
		return nil, err
	}
	if !center.ContractLive() || center.ModerationStatus != models.CenterModerationApproved {
		return nil, ErrCenterNotFound
	}
	return center, nil
}

// --- Rooms ↔ centre ---

// AttachRoomToCenter links a group to its centre. Admin-only; both must
// exist, and we refuse to move a room into a centre owned by someone
// else's account unless an admin is explicitly doing it.
func AttachRoomToCenter(roomID uuid.UUID, centerID *uuid.UUID) error {
	if centerID != nil {
		if _, err := GetCenterByID(*centerID); err != nil {
			return err
		}
	}
	return database.DB.Model(&models.Room{}).
		Where("id = ?", roomID).
		Update("center_id", centerID).Error
}

// CenterRooms lists a centre's groups. This is the seed of the
// multi-group dashboard: today it usually returns one row.
func CenterRooms(centerID uuid.UUID) ([]models.Room, error) {
	var rooms []models.Room
	err := database.DB.
		Where("center_id = ?", centerID).
		Order("created_at ASC").
		Find(&rooms).Error
	return rooms, err
}
