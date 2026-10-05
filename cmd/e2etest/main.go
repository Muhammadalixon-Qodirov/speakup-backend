// Command e2etest drives the room + centre features against a running
// server over real HTTP and WebSocket, the way the Mini App would.
//
// It exists because unit tests cannot answer the only question that
// matters before handing a room to a teacher: does a student who taps
// Speak actually get matched, and does the teacher's panel show it?
//
// Run:
//
//	JWT_SECRET=... API=https://api.speak-up.uz \
//	TEACHER=<uuid> S1=<uuid> S2=<uuid> ADMIN=<uuid> go run ./cmd/e2etest
//
// This is a development tool, not part of the server. The Dockerfile
// builds only cmd/server, so it never ships.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/golang-jwt/jwt/v5"
)

var (
	api     = env("API", "https://api.speak-up.uz")
	secret  = os.Getenv("JWT_SECRET")
	teacher = os.Getenv("TEACHER")
	stud1   = os.Getenv("S1")
	stud2   = os.Getenv("S2")
	admin   = os.Getenv("ADMIN")

	passed, failed int
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func check(name string, ok bool, detail string) {
	if ok {
		passed++
		fmt.Printf("  ✅ %s\n", name)
		return
	}
	failed++
	fmt.Printf("  ❌ %s\n     → %s\n", name, detail)
}

// --- auth ---

func token(userID string) string {
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": userID,
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
		"src": "miniapp",
	})
	s, err := t.SignedString([]byte(secret))
	if err != nil {
		panic(err)
	}
	return s
}

// --- REST ---

func req(method, path, userID string, body interface{}) (int, map[string]interface{}) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	r, _ := http.NewRequest(method, api+path, rdr)
	r.Header.Set("Authorization", "Bearer "+token(userID))
	r.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(r)
	if err != nil {
		return 0, map[string]interface{}{"error": err.Error()}
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	var out map[string]interface{}
	json.Unmarshal(raw, &out)
	if out == nil {
		out = map[string]interface{}{"raw": string(raw)}
	}
	return resp.StatusCode, out
}

func data(m map[string]interface{}) map[string]interface{} {
	if d, ok := m["data"].(map[string]interface{}); ok {
		return d
	}
	return map[string]interface{}{}
}

// --- WebSocket client ---

type wsClient struct {
	name string
	conn *websocket.Conn
	mu   sync.Mutex
	seen []map[string]interface{}
}

func dial(name, userID string) *wsClient {
	u := strings.Replace(api, "https://", "wss://", 1) + "/ws?token=" + token(userID)
	c, _, err := websocket.DefaultDialer.Dial(u, http.Header{"Origin": []string{"https://speak-up.uz"}})
	if err != nil {
		fmt.Printf("  ❌ %s ulanolmadi: %v\n", name, err)
		failed++
		return nil
	}
	w := &wsClient{name: name, conn: c}
	go func() {
		for {
			_, raw, err := c.ReadMessage()
			if err != nil {
				return
			}
			var m map[string]interface{}
			if json.Unmarshal(raw, &m) == nil {
				w.mu.Lock()
				w.seen = append(w.seen, m)
				w.mu.Unlock()
			}
		}
	}()
	return w
}

func (w *wsClient) send(event string, payload interface{}) {
	if w == nil {
		return
	}
	b, _ := json.Marshal(map[string]interface{}{"event": event, "data": payload})
	w.mu.Lock()
	w.conn.WriteMessage(websocket.TextMessage, b)
	w.mu.Unlock()
}

// await waits up to `d` for an event of the given type and returns its data.
func (w *wsClient) await(event string, d time.Duration) map[string]interface{} {
	if w == nil {
		return nil
	}
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		for _, m := range w.seen {
			if m["event"] == event {
				out, _ := m["data"].(map[string]interface{})
				w.mu.Unlock()
				return out
			}
		}
		w.mu.Unlock()
		time.Sleep(150 * time.Millisecond)
	}
	return nil
}

// awaitAny returns the first event seen from `events`, plus its name.
// Used where a silent failure would otherwise be indistinguishable from
// a slow one - the server may legitimately answer with a different event
// (channel gate, room_error) and we want the REASON, not a timeout.
func (w *wsClient) awaitAny(events []string, d time.Duration) (string, map[string]interface{}) {
	if w == nil {
		return "", nil
	}
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		for _, m := range w.seen {
			ev, _ := m["event"].(string)
			for _, want := range events {
				if ev == want {
					out, _ := m["data"].(map[string]interface{})
					w.mu.Unlock()
					return ev, out
				}
			}
		}
		w.mu.Unlock()
		time.Sleep(150 * time.Millisecond)
	}
	return "", nil
}

func (w *wsClient) forget() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.seen = nil
	w.mu.Unlock()
}

func (w *wsClient) close() {
	if w != nil {
		w.conn.Close()
	}
}

func main() {
	if secret == "" || teacher == "" || stud1 == "" || stud2 == "" || admin == "" {
		fmt.Println("JWT_SECRET, TEACHER, S1, S2, ADMIN kerak")
		os.Exit(2)
	}

	fmt.Println("\n═══ 1. ADMIN ROOM OCHADI ═══")
	code, resp := req("POST", "/admin/rooms", admin, map[string]interface{}{
		"owner_id":    teacher,
		"name":        "E2E TEST guruh",
		"description": "avtomatik test",
		"max_members": 10,
	})
	d := data(resp)
	room, _ := d["room"].(map[string]interface{})
	center, _ := d["center"].(map[string]interface{})
	check("POST /admin/rooms → 201", code == 201, fmt.Sprintf("kod=%d resp=%v", code, resp))
	if room == nil {
		fmt.Println("room yaratilmadi, to'xtatildi")
		os.Exit(1)
	}
	roomID, _ := room["id"].(string)
	roomCode, _ := room["code"].(string)
	invite, _ := room["invite_link"].(string)
	check("room kodi va invite link bor", roomCode != "" && strings.Contains(invite, roomCode),
		fmt.Sprintf("code=%q link=%q", roomCode, invite))
	check("markaz avtomatik yaratildi (logo uchun)", center != nil, "center yo'q")

	fmt.Println("\n═══ 2. O'QITUVCHI O'Z ROOMINI KO'RADI ═══")
	code, resp = req("GET", "/rooms/mine", teacher, nil)
	d = data(resp)
	owned, _ := d["owned"].([]interface{})
	check("GET /rooms/mine → owned[] bo'sh emas", code == 200 && len(owned) > 0,
		fmt.Sprintf("kod=%d owned=%d", code, len(owned)))

	fmt.Println("\n═══ 3. O'QUVCHILAR LINK ORQALI KIRADI ═══")
	for i, s := range []string{stud1, stud2} {
		code, resp = req("POST", "/rooms/join", s, map[string]string{"code": roomCode})
		check(fmt.Sprintf("o'quvchi %d qo'shildi", i+1), code == 200, fmt.Sprintf("kod=%d %v", code, resp))
	}
	// Idempotentlik: qayta bosish xato bermasligi kerak
	code, _ = req("POST", "/rooms/join", stud1, map[string]string{"code": roomCode})
	check("qayta kirish idempotent (xato emas)", code == 200, fmt.Sprintf("kod=%d", code))

	// Begona odam kira olmasligi kerak
	code, _ = req("GET", "/rooms/"+roomID+"/students", stud1, nil)
	check("o'quvchi hisobotni KO'RA OLMAYDI (403)", code == 403, fmt.Sprintf("kod=%d", code))

	fmt.Println("\n═══ 4. WEBSOCKET: SPEAK BOSIB JUFTLANISH ═══")
	wt := dial("o'qituvchi", teacher)
	w1 := dial("o'quvchi1", stud1)
	w2 := dial("o'quvchi2", stud2)
	defer wt.close()
	defer w1.close()
	defer w2.close()
	time.Sleep(1 * time.Second)
	check("3 ta WebSocket ulandi", wt != nil && w1 != nil && w2 != nil, "ulanish muvaffaqiyatsiz")

	w1.send("room_join_queue", map[string]string{"room_id": roomID})
	ev, q := w1.awaitAny([]string{"room_queued", "channel_subscription_required", "room_error", "limit_reached"}, 8*time.Second)
	check("1-o'quvchi navbatga tushdi (room_queued)", ev == "room_queued",
		fmt.Sprintf("kutilgan room_queued, kelgan: %q %v", ev, q))

	w2.send("room_join_queue", map[string]string{"room_id": roomID})
	m1 := w1.await("match_found", 10*time.Second)
	m2 := w2.await("match_found", 10*time.Second)
	check("1-o'quvchi juftlandi (match_found)", m1 != nil, "match_found kelmadi")
	check("2-o'quvchi juftlandi (match_found)", m2 != nil, "match_found kelmadi")

	var sessionID string
	if m1 != nil && m2 != nil {
		s1id, _ := m1["session_id"].(string)
		s2id, _ := m2["session_id"].(string)
		sessionID = s1id
		check("ikkalasi BIR sessiyada", s1id == s2id && s1id != "", fmt.Sprintf("%q vs %q", s1id, s2id))
		rid, _ := m1["room_id"].(string)
		check("match_found ichida room_id bor", rid == roomID, fmt.Sprintf("room_id=%q", rid))
		c1, _ := m1["is_caller"].(bool)
		c2, _ := m2["is_caller"].(bool)
		check("faqat bittasi caller (WebRTC uchun)", c1 != c2, fmt.Sprintf("%v/%v", c1, c2))
		lim, _ := m1["limit_minutes"].(float64)
		check("room limiti 120 daqiqa (kunlik limit emas)", int(lim) == 120, fmt.Sprintf("limit=%v", lim))
	}

	fmt.Println("\n═══ 5. O'QITUVCHI PANELI (jonli) ═══")
	// O'qituvchi navbat o'zgarishlarida allaqachon room_state olgan
	// (juftlashdan OLDIN, speaking_count=0 bilan). Eskisini tashlab,
	// yangisini so'raymiz - aks holda test eski suratni tekshiradi.
	wt.forget()
	time.Sleep(500 * time.Millisecond)
	wt.send("room_state", map[string]string{"room_id": roomID})
	st := wt.await("room_state", 6*time.Second)
	check("o'qituvchiga room_state keldi", st != nil, "room_state kelmadi")
	if st != nil {
		sc, _ := st["speaking_count"].(float64)
		check("panelda 2 kishi gaplashyapti", int(sc) == 2, fmt.Sprintf("speaking_count=%v", sc))
		as, _ := st["active_sessions"].([]interface{})
		check("active_sessions ro'yxati bor (tinglash uchun)", len(as) == 1, fmt.Sprintf("%d ta", len(as)))
		mem, _ := st["members"].([]interface{})
		check("a'zolar ro'yxati (3 kishi)", len(mem) == 3, fmt.Sprintf("%d ta", len(mem)))
	}

	fmt.Println("\n═══ 6. O'QITUVCHI TINGLAYDI ═══")
	w1.forget()
	w2.forget()
	wt.send("room_listen_start", map[string]string{"session_id": sessionID})
	ls := wt.await("listen_started", 6*time.Second)
	check("o'qituvchiga listen_started keldi", ls != nil, "listen_started kelmadi")
	lj1 := w1.await("listener_joined", 6*time.Second)
	lj2 := w2.await("listener_joined", 6*time.Second)
	check("1-o'quvchi OGOHLANTIRILDI (listener_joined)", lj1 != nil, "listener_joined kelmadi")
	check("2-o'quvchi OGOHLANTIRILDI (listener_joined)", lj2 != nil, "listener_joined kelmadi")

	// Begona odam tinglay olmasligi kerak
	w1.forget()
	w1.send("room_listen_start", map[string]string{"session_id": sessionID})
	re := w1.await("room_error", 5*time.Second)
	check("o'quvchi TINGLAY OLMAYDI (room_error)", re != nil, "xato qaytmadi — himoya ishlamadi!")

	wt.send("room_listen_stop", map[string]interface{}{})
	w1.forget()
	ll := w1.await("listener_left", 6*time.Second)
	check("tinglash to'xtadi (listener_left)", ll != nil, "listener_left kelmadi")

	fmt.Println("\n═══ 7. SUHBAT TUGADI ═══")
	w1.forget()
	w2.forget()
	w1.send("session_end", map[string]string{"session_id": sessionID})
	e1 := w1.await("session_ended", 8*time.Second)
	e2 := w2.await("session_ended", 8*time.Second)
	check("1-o'quvchiga session_ended", e1 != nil, "kelmadi")
	check("2-o'quvchiga session_ended", e2 != nil, "kelmadi")
	if e1 != nil {
		rid, _ := e1["room_id"].(string)
		check("session_ended ichida room_id bor", rid == roomID, fmt.Sprintf("room_id=%v", e1["room_id"]))
	}

	fmt.Println("\n═══ 8. QO'LDA JUFTLASH ═══")
	time.Sleep(1500 * time.Millisecond)
	wt.forget()
	wt.send("room_pair", map[string]interface{}{
		"room_id": roomID,
		"pairs":   []map[string]string{{"user1_id": stud1, "user2_id": stud2}},
	})
	pr := wt.await("room_paired", 8*time.Second)
	check("room_paired javobi keldi", pr != nil, "kelmadi")
	if pr != nil {
		created, _ := pr["created"].(float64)
		check("1 juftlik yaratildi", int(created) == 1, fmt.Sprintf("created=%v results=%v", created, pr["results"]))
	}
	// tozalash uchun sessiyani tugatamiz
	w1.forget()
	m3 := w1.await("match_found", 8*time.Second)
	if m3 != nil {
		if sid, ok := m3["session_id"].(string); ok {
			w1.send("session_end", map[string]string{"session_id": sid})
			time.Sleep(1500 * time.Millisecond)
		}
	}

	fmt.Println("\n═══ 9. O'QITUVCHI HISOBOTLARI ═══")
	time.Sleep(2 * time.Second) // hisoblagichlar yozilishiga ulgursin
	code, resp = req("GET", "/rooms/"+roomID+"/students", teacher, nil)
	arr, _ := resp["data"].([]interface{})
	check("GET /students → 200", code == 200, fmt.Sprintf("kod=%d", code))
	check("hisobotda 3 ta a'zo (faolsizlar ham)", len(arr) == 3, fmt.Sprintf("%d ta", len(arr)))
	foundMinutes := false
	for _, x := range arr {
		r, _ := x.(map[string]interface{})
		if s, _ := r["room_sessions"].(float64); s > 0 {
			foundMinutes = true
		}
	}
	check("sessiyalar hisobga olindi", foundMinutes, "hech kimda room_sessions>0 yo'q")

	code, resp = req("GET", "/rooms/"+roomID+"/attendance?days=3", teacher, nil)
	d = data(resp)
	days, _ := d["days"].([]interface{})
	check("GET /attendance → 3 kunlik qator", code == 200 && len(days) == 3, fmt.Sprintf("kod=%d kun=%d", code, len(days)))

	code, resp = req("GET", "/rooms/"+roomID+"/report?days=7", teacher, nil)
	check("GET /report → 200", code == 200, fmt.Sprintf("kod=%d", code))

	fmt.Println("\n═══ 10. MARKAZ PROFILI VA BANNER ═══")
	code, resp = req("GET", "/centers/mine", teacher, nil)
	check("GET /centers/mine → 200", code == 200, fmt.Sprintf("kod=%d %v", code, resp))

	code, resp = req("PUT", "/centers/mine", teacher, map[string]interface{}{
		"phone":         "+998901234567",
		"instagram_url": "instagram.com/testmarkaz",
		"about":         "E2E test markazi",
	})
	check("PUT /centers/mine → 200", code == 200, fmt.Sprintf("kod=%d %v", code, resp))
	d = data(resp)
	if c, ok := d["center"].(map[string]interface{}); ok {
		ig, _ := c["instagram_url"].(string)
		check("havola https:// bilan normallashtirildi", strings.HasPrefix(ig, "https://"), fmt.Sprintf("ig=%q", ig))
	}
	nr, _ := d["needs_review"].(bool)
	check("about o'zgargani uchun moderatsiya kerak emas (draft)", !nr, "draft holatda review talab qilindi")

	// Xavfli havola rad etilishi kerak
	code, _ = req("PUT", "/centers/mine", teacher, map[string]interface{}{
		"website_url": "javascript:alert(1)",
	})
	check("javascript: havola RAD ETILDI", code == 400, fmt.Sprintf("kod=%d", code))

	code, resp = req("GET", "/session-banners", stud1, nil)
	check("GET /session-banners → 200", code == 200, fmt.Sprintf("kod=%d", code))

	fmt.Printf("\n═══════════════════════════════\n  O'TDI: %d    YIQILDI: %d\n═══════════════════════════════\n", passed, failed)
	fmt.Printf("ROOM_ID=%s\n", roomID)
	if failed > 0 {
		os.Exit(1)
	}
}
