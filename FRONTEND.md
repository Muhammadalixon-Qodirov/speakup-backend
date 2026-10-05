# Frontend uchun qo'llanma — 2026-08-01 o'zgarishlari

Backend'ga **Room** (o'qituvchi uchun yopiq speaking guruhi) va **O'quv
markaz** (profil + sessiya ichidagi banner reklama) qo'shildi.

Barcha yangi endpointlar **jonli serverda** sinalgan: `api.speak-up.uz`,
41/41 test o'tdi.

**Mavjud funksiyalarning hech biri buzilmagan.** Umumiy navbat, WebRTC,
mini-o'yinlar, AI test — hammasi avvalgidek. Faqat qo'shimchalar bor.

---

## 🆕 2026-10-05: Talaffuz mashqi (o'qib berish)

Yangi bo'lim qo'shildi — foydalanuvchi matnni ovoz chiqarib o'qiydi, so'zma-so'z
talaffuz natijasini oladi. **Alohida hujjatda:**

### → [`docs/FRONTEND_TALAFFUZ.md`](docs/FRONTEND_TALAFFUZ.md)

Ikki narsa boshidan bilinishi kerak:

1. **Audio 16 kHz mono WAV bo'lishi shart.** `MediaRecorder` beradigan WebM
   qabul qilinmaydi. Tayyor konvertatsiya kodi hujjatda bor.
2. **Natija "hukm" emas, "maslahat".** Ball, foiz, qizil ❌ ishlatilmaydi —
   sababi hujjatda raqamlar bilan tushuntirilgan.

---

---

## 0. Umumiy qoidalar (avval shuni o'qing)

### Javob konverti

**Har bir** REST javob shu qobiqda keladi. Pastdagi barcha misollarda
faqat `data` ichi ko'rsatilgan:

```jsonc
// muvaffaqiyat
{ "success": true, "data": { ... } }

// xato
{ "success": false, "code": "ROOM_CLOSED", "error": "Room yopilgan" }
```

`code` faqat ba'zi xatolarda bo'ladi (9-bo'limdagi jadval). Bo'lmasa
`error` matnini ko'rsating.

### Tezlik cheklovlari (429 olmaslik uchun)

| Endpoint | Limit |
|---|---|
| `POST /rooms/join` | 10 / daqiqa |
| `GET /rooms/preview/:code` | 20 / daqiqa |
| `POST /rooms/:id/regenerate-code` | 5 / daqiqa |
| `POST /centers/mine/logo` | 10 / daqiqa |
| `POST /centers/mine/submit` | 10 / daqiqa |
| Qolgan barcha auth'li endpointlar | 200 / daqiqa (har user) |

429 kelsa **darhol qayta urinmang** — foydalanuvchiga xabar bering.

### Boshqa maydalar

- **Sana formati:** hamma joyda RFC3339 (`2026-08-01T15:20:00Z`), UTC.
  Ko'rsatishda Toshkent vaqtiga o'giring (+5).
- **Pagination:** `?limit=&offset=`. Default `limit=50`, maksimum `200`.
- **Rasm manzillari** (`logo_url`) nisbiy: `https://api.speak-up.uz` +
  `logo_url`. `<img src>` bilan ham, `fetch()` bilan ham ishlaydi
  (CORS sarlavhalari yuboriladi).
- **HTTP kodlari:** `200` OK, `201` yaratildi (`POST /admin/rooms`),
  `400` noto'g'ri so'rov, `401` token yaroqsiz, `403` ruxsat yo'q,
  `404` topilmadi, `429` juda ko'p so'rov.

### ⚠️ Mavjud endpointdagi xatti-harakat o'zgarishi

**`GET /users/daily-usage`** — `today_minutes` maydoni endi **room
daqiqalarini ham qo'shib** qaytaradi.

```jsonc
{ "minutes_used": 6,        // faqat umumiy navbat (kunlik limitga kiradi)
  "minutes_limit": 10, "minutes_left": 4,
  "today_minutes": 51,      // ← umumiy + room (streak va progress uchun)
  "streak_threshold": 10 }
```

Ya'ni bosh sahifadagi kunlik progress endi dars vaqtini ham ko'rsatadi.
`minutes_used` / `minutes_left` esa avvalgidek **faqat** umumiy navbatni
hisoblaydi. Ikkalasini aralashtirib yubormang:

- **Limit ko'rsatkichi** → `minutes_left`
- **Streak progressi** → `today_minutes` / `streak_threshold`

---

## Mundarija

1. [Eng muhim 3 ta o'zgarish](#1-eng-muhim-3-ta-ozgarish)
2. [Auth — deep link bilan avtomatik kirish](#2-auth--deep-link-bilan-avtomatik-kirish)
3. [Room — o'quvchi tomoni](#3-room--oquvchi-tomoni)
4. [Room — o'qituvchi tomoni](#4-room--oqituvchi-tomoni)
5. [WebSocket kontrakti](#5-websocket-kontrakti)
6. [Tinglash — WebRTC](#6-tinglash--webrtc)
7. [Markaz profili va banner](#7-markaz-profili-va-banner)
8. [Admin panel](#8-admin-panel)
9. [Xato kodlari](#9-xato-kodlari)
10. [Qurilishi kerak bo'lgan ekranlar](#10-qurilishi-kerak-bolgan-ekranlar)

---

## 1. Eng muhim 3 ta o'zgarish

### a) `match_found` ga 2 ta maydon qo'shildi

Shakli **o'zgarmagan** — WebRTC kodingizga tegish shart emas:

```jsonc
{
  "session_id": "...",
  "partner": { "id": "...", "name": "Ali", "level": "B1",
               "region": "...", "photo_url": null,
               "pinned_speaking_band": null },
  "topic": "...",
  "limit_minutes": 120,        // room'da doim 120, umumiyda kunlik qoldiq
  "is_caller": true,
  "room_id": "8085b710-...",   // ← YANGI. null bo'lsa oddiy sessiya
  "room_name": "IELTS B2"      // ← YANGI
}
```

`room_id != null` bo'lsa: "Dars sessiyasi" belgisini ko'rsating va suhbat
tugagach **room ekraniga** qayting (baholash ekraniga emas).

### b) `session_ended` ga `room_id` qo'shildi

```jsonc
{ "session_id": "...", "duration_minutes": 12,
  "ended_by": "you" | "partner",
  "reason": "disconnected" | "removed_from_room",   // ixtiyoriy
  "room_id": "8085b710-..." | null }                // ← YANGI
```

### c) `/auth/telegram` javobiga `joined_room` qo'shildi

Foydalanuvchi o'qituvchining linki bilan kirsa, **login paytida avtomatik
roomga qo'shiladi**:

```jsonc
{ "success": true,
  "data": {
    "token": "...", "user": { ... },
    "joined_room": { "id": "...", "name": "IELTS B2" }   // ← YANGI, ixtiyoriy
  } }
```

Bor bo'lsa — darhol o'sha room ekraniga o'tkazing.

---

## 2. Auth — deep link bilan avtomatik kirish

O'qituvchi tarqatadigan link:

```
https://t.me/speakupuzbot?startapp=room_ABC12345
```

Bosilganda Mini App ochiladi, `initData.start_param = "room_ABC12345"`
bo'ladi va backend uni o'zi qayta ishlaydi. **Frontend hech narsa
qilmaydi** — faqat javobdagi `joined_room` ni tekshiradi.

### Muqobil shakl

Agar link `?start=room_ABC12345` bo'lsa (oddiy bot chati), bot Mini App
tugmasini ko'rsatadi va ilova `?room=ABC12345` bilan ochiladi. Bu holda
**frontend o'zi** chaqirishi kerak:

```js
const code = new URLSearchParams(location.search).get('room');
if (code) await api.post('/rooms/join', { code });
```

Ikkala yo'lni ham qo'llab-quvvatlang — ikkitasi ham amalda uchraydi.

---

## 3. Room — o'quvchi tomoni

### `GET /rooms/mine`

Profil bo'limi uchun. Ikki ro'yxat qaytaradi.

```jsonc
{ "success": true, "data": {
    "owned":  [ /* men egasi bo'lgan roomlar (o'qituvchi) */ ],
    "joined": [ /* men a'zo bo'lgan roomlar (o'quvchi) */ ]
} }
```

Har bir room:

```jsonc
{
  "id": "8085b710-...", "name": "IELTS B2", "description": "Du/Ju 18:00",
  "owner_id": "...", "owner_name": "Aziz",
  "is_active": true,
  "is_open": true,           // muzlatilgan yoki muddati o'tgan bo'lsa false
  "member_count": 12, "max_members": 100,
  "total_sessions": 340, "total_minutes": 2100,
  "expires_at": null, "created_at": "...",
  "is_owner": true,

  // ↓ FAQAT EGASIGA keladi — bu roomning paroli, ko'rsatishda ehtiyot bo'ling
  "code": "ABC12345",
  "invite_link": "https://t.me/speakupuzbot?startapp=room_ABC12345"
}
```

> `owned` bo'sh bo'lmasa — foydalanuvchi o'qituvchi. Profilda "Mening
> roomim" kartasini va invite link'ni nusxalash tugmasini ko'rsating.

### `GET /rooms/preview/:code`

Kirishdan **oldingi** ko'rinish (kod bor, lekin hali a'zo emas):

```jsonc
{ "id": "...", "name": "IELTS B2", "description": "...",
  "owner_name": "Aziz", "member_count": 12, "max_members": 100,
  "is_open": true, "already_member": false }
```

### `POST /rooms/join`

```jsonc
// so'rov
{ "code": "ABC12345" }        // "room_ABC12345" ham qabul qilinadi
// javob: room obyekti (yuqoridagi shakl)
```

**Idempotent** — qayta bosilsa xato bermaydi, 200 qaytaradi. O'quvchilar
har dars link bosib ilovani ochadi, shuning uchun shunday qilingan.

### `GET /rooms/:id`

Room ekranining boshlang'ich yuklamasi:

```jsonc
{ "room": { /* yuqoridagi shakl */ },
  "state": { /* room_state — 5-bo'limga qarang */ } }
```

Keyin WebSocket'dagi `room_state` ga obuna bo'ling — panel o'zi yangilanadi.

### `GET /rooms/:id/members`

**Har bir a'zoga** ochiq — o'quvchi guruhdoshlarini ko'rishni xohlaydi.
`room_state.members[]` bilan bir xil massiv qaytaradi:

```jsonc
[ { "user_id": "...", "name": "Ali", "photo_url": null, "level": "B1",
    "role": "student", "joined_at": "...",
    "total_sessions": 14, "total_minutes": 96, "last_spoke_at": "...",
    "online": true, "searching": false, "speaking": false } ]
```

> Farqi: `/rooms/:id/students` — **faqat egaga**, ichida streak va AI
> ballari bor. `/members` — hammaga, faqat ism va holat.

### `POST /rooms/:id/leave`

Roomdan chiqish. **Egasi chiqa olmaydi** → `OWNER_CANT_LEAVE`.

---

## 4. Room — o'qituvchi tomoni

Hammasi **faqat egaga** (yoki platforma adminiga). Boshqasi `403 NOT_OWNER`.

### `GET /rooms/:id/students` — asosiy jadval

"Bugun kim keldi, kim kelmadi" degan savolga javob.

```jsonc
[
  { "user_id": "...", "name": "Ali", "photo_url": null, "level": "B1",
    "role": "student",
    "room_sessions": 14, "room_minutes": 96,
    "last_spoke_at": "2026-08-01T15:20:00Z",
    "today_sessions": 2, "today_minutes": 11,   // ← davomat
    "current_streak": 5,
    "ai_tests": 3, "latest_band": 6.0, "best_band": 6.5 }
]
```

Faoliyat qilmaganlar ham **nol bilan** keladi — ular tushib qolsa
"kim mashq qilmadi" savoliga javob bo'lmasdi.

### `GET /rooms/:id/students/:user_id` — bitta o'quvchi kartasi

```jsonc
{ "user_id": "...", "name": "Ali", "level": "B1", "role": "student",
  "joined_at": "...",
  "room_sessions": 14, "room_minutes": 96, "last_spoke_at": "...",
  "today_sessions": 2, "today_minutes": 11,
  "total_sessions": 41, "total_minutes": 260,   // platforma bo'yicha
  "current_streak": 5, "max_streak": 12,
  "best_band": 6.5, "latest_band": 6.0,
  "ai_results": [
    { "kind": "mock",     // "practice" = oddiy AI check, "mock" = 3 qismli IELTS
      "report_id": "...", "topic": "Technology",
      "overall_band": 6.5,
      "fluency": 6.5, "lexical": 6.0, "grammar": 6.5, "pronunciation": 7.0,
      "created_at": "..." }
  ] }
```

`?ai_limit=20` bilan AI natijalari sonini boshqarsa bo'ladi.

> **Transkript berilmaydi** — ataylab. Ball va mezonlar o'rgatish uchun
> yetarli; o'quvchi aynan nima deganining matni uning o'zi ulashadigan narsa.

### `GET /rooms/:id/attendance?days=14` — grafik

```jsonc
{ "days": [
    { "date": "2026-07-30", "active_students": 8, "sessions": 12, "minutes": 84 },
    { "date": "2026-07-31", "active_students": 0, "sessions": 0,  "minutes": 0  }
] }
```

Bo'sh kunlar ham qaytadi — grafik ularni o'tkazib yuborsa, aynan bo'shliqlar
ko'rinmay qoladi.

### `GET /rooms/:id/report?days=7`

```jsonc
{ "days": 7, "since": "...", "until": "...",
  "rows": [ { "user_id": "...", "name": "Ali", "role": "student",
              "sessions": 9, "minutes": 61, "days": 4 } ] }
```

`days` — necha **kun** kelgani (bir marta uzoq gaplashish bir hafta mashq
qilgandek ko'rinmasligi uchun).

### `GET /rooms/:id/sessions?limit=50&offset=0` — dars jurnali

```jsonc
{ "sessions": [ { "session_id": "...", "user1_id": "...", "user1_name": "Ali",
                  "user2_id": "...", "user2_name": "Vali", "topic": "...",
                  "status": "ended", "duration_minutes": 7,
                  "started_at": "...", "ended_at": "..." } ],
  "total": 340, "limit": 50, "offset": 0 }
```

### Boshqarish

| Method | Yo'l | Nima |
|---|---|---|
| `PUT` | `/rooms/:id` | `{name?, description?, is_active?}` — nom, tavsif, **muzlatish** |
| `POST` | `/rooms/:id/regenerate-code` | **Linkni yangilash** (eski linklar darhol o'lik bo'ladi, a'zolar qoladi) |
| `DELETE` | `/rooms/:id/members/:user_id` | O'quvchini chiqarish — **jonli suhbati ham uziladi** |

`is_active: false` qilinganda navbat ham tozalanadi.

---

## 5. WebSocket kontrakti

Mavjud `/ws?token=<JWT>` soketi. Format o'zgarmagan:
`{"event": "...", "data": {...}}`.

### Client → server

| Event | Data | Kim |
|---|---|---|
| `room_join_queue` | `{room_id}` | a'zo — **Speak bosildi** |
| `room_leave_queue` | `{}` | a'zo |
| `room_state` | `{room_id}` | a'zo — panelni yangilash |
| `room_start_round` | `{room_id}` | **ega** — hammani tasodifiy juftlaydi |
| `room_pair` | `{room_id, pairs:[{user1_id,user2_id}]}` | **ega** — o'zi tanlagan juftliklar |
| `room_listen_start` | `{session_id}` | **ega** — tinglash |
| `room_listen_stop` | `{}` | **ega** |
| `listen_offer` / `listen_answer` / `listen_ice` | `{session_id, target_id, offer\|answer\|candidate}` | 6-bo'limga qarang |

### Server → client

| Event | Data |
|---|---|
| `room_queued` | `{room_id, room_name, queue_size, note?}` |
| `room_queue_left` | `{room_id}` |
| `match_found` | oddiy + `room_id`, `room_name` |
| `room_state` | pastda |
| `room_round_started` | `{room_id, pairs, leftover}` |
| `room_paired` | `{room_id, created, results:[{user1_id,user2_id,ok,reason?}]}` |
| `room_error` | `{code, message}` |
| `listen_started` | `{session_id, room_id, participants:[{id,name}]}` → **egaga** |
| `listen_stopped` | `{session_id, reason:"you"\|"session_ended"}` → **egaga** |
| `listener_joined` | `{session_id, listener_id, listener_name}` → **o'quvchilarga** |
| `listener_left` | `{session_id, listener_id}` → **o'quvchilarga** |
| `channel_subscription_required` | `{channel}` — kanal obunasi kerak |

### `room_state` to'liq shakli

```jsonc
{
  "room_id": "...", "room_name": "IELTS B2", "owner_id": "...",
  "member_count": 12, "online_count": 5,
  "queue_size": 2,        // hozir Speak bosib kutayotganlar
  "speaking_count": 4,    // hozir gaplashayotganlar
  "total_sessions": 340, "total_minutes": 2100,

  "members": [
    { "user_id": "...", "name": "Ali", "photo_url": null, "level": "B1",
      "role": "student", "joined_at": "...",
      "total_sessions": 14, "total_minutes": 96, "last_spoke_at": "...",
      "online": true, "searching": false, "speaking": true }
  ],

  "active_sessions": [        // o'qituvchining "tinglash" menyusi
    { "session_id": "...",
      "user1_id": "...", "user1_name": "Ali",
      "user2_id": "...", "user2_name": "Vali",
      "topic": "...", "started_at": "...",
      "listeners": ["<o'qituvchi_id>"] }
  ]
}
```

`room_state` **avtomatik push** bo'ladi: kimdir navbatga kirsa/chiqsa,
juftlansa, suhbat tugasa, uzilib qolsa, tinglash boshlansa/tugasa.
O'qituvchi paneli shu bitta eventga obuna bo'lsa yetarli — polling
qilmang.

> ⚠️ `room_state` ni **oxirgisini** oling. Bir necha xabar ketma-ket
> kelishi mumkin; birinchisini emas, eng yangisini ishlating.

### ⚠️ Navbat 2 daqiqadan keyin o'chadi

`room_join_queue` yuborilgandan keyin foydalanuvchi navbatda **120
soniya** turadi. Bu muddat ichida hech kim qo'shilmasa, u navbatdan
**jimgina** chiqariladi — hech qanday event kelmaydi.

Sabab: Speak bosib telefonini cho'ntagiga solgan o'quvchi butun dars
davomida "bo'sh" bo'lib ko'rinmasligi kerak.

Frontend nima qilishi kerak:

```js
// Speak bosilgandi
send('room_join_queue', { room_id });
const timer = setInterval(() => send('room_join_queue', { room_id }), 90_000);

// match_found / room_queue_left / ekrandan chiqish → clearInterval(timer)
```

Ya'ni **90 soniyada bir marta qayta yuboring** (TTL tugashidan oldin).
Qayta yuborish xavfsiz — foydalanuvchi allaqachon navbatda bo'lsa, o'rni
yangilanadi xolos.

Umumiy navbat (`join_queue`) ham xuddi shunday ishlaydi — agar u yerda
allaqachon shunday qilgan bo'lsangiz, o'sha mantiqni qayta ishlating.

### Muhim: bir vaqtda bitta navbat

`room_join_queue` yuborilsa foydalanuvchi umumiy navbatdan chiqariladi va
aksincha. Ya'ni "Speak" tugmasi qaysi ekranda bosilgani muhim — ikkalasini
bir vaqtda yubormang.

---

## 6. Tinglash — WebRTC

O'quvchilarning bir-biri bilan ulanishiga **tegilmaydi**. Uning ustiga har
bir o'quvchi o'qituvchi tomon **ikkinchi, faqat-yuboruvchi** ulanish ochadi.

```
   O'quvchi A  ⇄  O'quvchi B      ← mavjud suhbat, o'zgarmaydi
        ↓              ↓
        └──→ O'qituvchi ←──┘      ← 2 ta yangi sendonly ulanish
```

### O'quvchi tomonida

```js
socket.on('listener_joined', async ({ session_id, listener_id, listener_name }) => {
  showBadge(`🎧 ${listener_name} tinglayapti`);      // ← KO'RSATISH SHART

  const pc = new RTCPeerConnection({ iceServers });
  listenPCs.set(listener_id, pc);

  // Faqat yuboramiz, qabul qilmaymiz
  localStream.getAudioTracks().forEach(t => pc.addTrack(t, localStream));

  pc.onicecandidate = e => e.candidate && send('listen_ice', {
    session_id, target_id: listener_id, candidate: e.candidate
  });

  const offer = await pc.createOffer();
  await pc.setLocalDescription(offer);
  send('listen_offer', { session_id, target_id: listener_id, offer });
});

socket.on('listen_answer', async ({ sender_id, answer }) => {
  await listenPCs.get(sender_id)?.setRemoteDescription(answer);
});

socket.on('listen_ice', async ({ sender_id, candidate }) => {
  await listenPCs.get(sender_id)?.addIceCandidate(candidate);
});

socket.on('listener_left', ({ listener_id }) => {
  listenPCs.get(listener_id)?.close();
  listenPCs.delete(listener_id);
  hideBadge();
});
```

### O'qituvchi tomonida

```js
send('room_listen_start', { session_id });

socket.on('listen_started', ({ participants }) => { /* 2 ta oqim kutamiz */ });

socket.on('listen_offer', async ({ session_id, sender_id, offer }) => {
  const pc = new RTCPeerConnection({ iceServers });
  pcs.set(sender_id, pc);
  pc.ontrack = e => attachAudio(sender_id, e.streams[0]);
  pc.onicecandidate = e => e.candidate && send('listen_ice', {
    session_id, target_id: sender_id, candidate: e.candidate
  });
  await pc.setRemoteDescription(offer);
  const answer = await pc.createAnswer();
  await pc.setLocalDescription(answer);
  send('listen_answer', { session_id, target_id: sender_id, answer });
});
```

### Qoidalar

- O'qituvchi **bir vaqtda faqat bitta** suhbatni tingaydi. Boshqasiga
  o'tsa avvalgisi avtomatik uziladi va u yerdagilarda indikator o'chadi.
- **Yashirin rejim yo'q.** `listener_joined` har doim yuboriladi va
  indikatorni ko'rsatish frontend zimmasida. Bu qasddan shunday: o'quvchi
  brauzeri baribir yangi ulanish ochadi, ya'ni yashirish yolg'on bo'lardi.
- Server faqat **tinglovchi ↔ o'sha suhbat ishtirokchisi** orasidagi
  xabarni o'tkazadi. Boshqasi jimgina tashlanadi.
- `iceServers` ni avvalgidek `GET /users/ice-config` dan oling.

---

## 7. Markaz profili va banner

Room ochilganda markaz profili **avtomatik yaratiladi**. O'qituvchi uchun
bu "Room sozlamalari" — "markaz" so'zini ko'rsatish shart emas.

### O'qituvchi tomoni

**`GET /centers/mine`**

```jsonc
{ "center": {
    "id": "...", "name": "IELTS Zone",
    "logo_url": "/uploads/centers/<id>.png" | null,
    "about": null, "courses": [],
    "phone": null, "address": null,
    "telegram_url": null, "instagram_url": null,
    "website_url": null, "signup_url": null,
    "moderation_status": "draft",     // draft|pending|approved|rejected
    "moderation_note": null,
    "is_active": true, "is_advertised": false,
    "contract_expires_at": null,
    "banner_impressions": 0, "banner_clicks": 0 },
  "rooms": [ { "id": "...", "name": "...", "member_count": 12, ... } ],
  "banner_eligible": false,
  "contract_live": true }
```

404 qaytsa — bu foydalanuvchida markaz yo'q, bo'limni ko'rsatmang.

**`PUT /centers/mine`** — barcha maydonlar ixtiyoriy, bittasini ham
yuborsa bo'ladi:

```jsonc
{ "name": "IELTS Zone", "about": "10 yillik tajriba",
  "phone": "+998901234567",
  "instagram_url": "instagram.com/ieltszone",   // https:// o'zi qo'shiladi
  "telegram_url": "t.me/ieltszone",
  "website_url": "...", "signup_url": "...",
  "address": "Chilonzor 5",
  "courses": [ { "title": "IELTS 6.5", "description": "3 oy",
                 "duration": "3 oy", "price": "450 000 so'm" } ] }
```

Javob:

```jsonc
{ "center": { ... }, "needs_review": true,
  "message": "Saqlandi. O'zgarish tasdiqlanguncha banner ko'rinmaydi." }
```

> **`needs_review` ni foydalanuvchiga ko'rsating.** `true` bo'lsa banner
> tasdiqlanguncha to'xtaydi. Nom / tavsif / kurslar / logotip o'zgarsa
> `true`, telefon / manzil / ijtimoiy tarmoq o'zgarsa `false`.

**`POST /centers/mine/logo`** — `multipart/form-data`, maydon nomi `logo`.

- Faqat **PNG yoki JPEG**, maksimum **2 MB**, maksimum **4000×4000**
- Server rasmni qayta kodlaydi (metama'lumot tozalanadi)
- Yuklangach profil avtomatik qayta moderatsiyaga tushadi

```jsonc
{ "logo_url": "/uploads/centers/<id>.png", "needs_review": true,
  "message": "Logotip yuklandi. Tasdiqlanguncha banner ko'rinmaydi." }
```

Xato: `400 BAD_LOGO` + tushunarli matn ("rasm hajmi 2 MB dan oshmasligi
kerak" kabi).

**`POST /centers/mine/submit`** — `draft`/`rejected` profilni moderatsiyaga
yuborish. Logotip va kamida bitta aloqa bo'lishi shart, aks holda 400.

### Banner — barcha foydalanuvchilar

**`GET /session-banners`** — sessiya boshlanganda **bir marta** chaqiring,
keyin ro'yxatni o'zingiz aylantiring (masalan har 8 soniyada).

```jsonc
[ { "id": "...", "name": "IELTS Zone",
    "logo_url": "/uploads/centers/<id>.png",
    "tagline": "10 yillik tajriba" } ]
```

Bo'sh massiv qaytishi normal — hech qaysi markaz reklama shartnomasida
bo'lmasa shunday bo'ladi. Bunda bannerni umuman ko'rsatmang.

**`GET /centers/:id`** — banner bosilganda ochiladigan karta:

```jsonc
{ "id": "...", "name": "...", "logo_url": "...", "about": "...",
  "courses": [...], "phone": "...", "address": "...",
  "telegram_url": "...", "instagram_url": "...",
  "website_url": "...", "signup_url": "..." }
```

**`POST /centers/:id/click`** — bosilganda yuboring. Javob:
`{ "counted": true }` (bir foydalanuvchi soatiga bir marta hisoblanadi).

> `logo_url` nisbiy yo'l. To'liq manzil: `https://api.speak-up.uz` + `logo_url`.

---

## 8. Admin panel

**Roomlar**

| Method | Yo'l | Nima |
|---|---|---|
| `GET` | `/admin/rooms?search=&limit=&offset=` | Ro'yxat (nom, ega ismi, kod bo'yicha qidiruv) |
| `POST` | `/admin/rooms` | **Room ochish** |
| `PUT` | `/admin/rooms/:id` | Tahrir / muzlatish / egasini almashtirish |
| `DELETE` | `/admin/rooms/:id` | O'chirish |
| `POST` | `/admin/rooms/:id/regenerate-code` | Kod yangilash |
| `GET` | `/admin/rooms/:id/members` · `/sessions` | A'zolar, sessiyalar |
| `PUT` | `/admin/rooms/:id/center` | `{center_id}` — markazga bog'lash |

Room ochish:

```jsonc
// POST /admin/rooms
{ "owner_telegram_id": 123456789,   // yoki "owner_id": "<uuid>"
  "name": "IELTS Zone — B2 guruh",
  "description": "Du/Ju 18:00",
  "max_members": 40,
  "expires_in_days": 90 }           // 0 yoki yo'q = muddatsiz

// javob (201)
{ "room": { ..., "invite_link": "https://t.me/..." },
  "center": { ... } }               // markaz avtomatik yaratiladi
```

`invite_link` ni markazga berasiz. Egasi ham uni `/rooms/mine` da ko'radi.

**Markazlar**

| Method | Yo'l | Nima |
|---|---|---|
| `GET` | `/admin/centers?status=pending` | **Moderatsiya navbati** + `pending_count` |
| `POST` | `/admin/centers` | Markaz ochish |
| `GET` | `/admin/centers/:id` | Batafsil + guruhlari |
| `POST` | `/admin/centers/:id/moderate` | `{approve: bool, note: string}` |
| `PUT` | `/admin/centers/:id/contract` | `{is_active?, is_advertised?, expires_in_days?}` |

Rad etishda `note` **majburiy** — markaz nimani tuzatishini bilishi kerak.

`is_advertised` — banner shartnoma sharti. Yangi markazda **o'chiq**
bo'ladi, admin qo'lda yoqadi.

---

## 9. Xato kodlari

`room_error` (WebSocket) va REST `code` maydoni:

| Kod | Ma'nosi | Nima ko'rsatish |
|---|---|---|
| `ROOM_NOT_FOUND` | Room yo'q yoki kod noto'g'ri | "Room topilmadi" |
| `ROOM_CLOSED` | Muzlatilgan yoki muddati o'tgan | "Room yopilgan" |
| `ROOM_FULL` | Joy tugagan | "Room to'lgan" |
| `NOT_A_MEMBER` | A'zo emas | Kirish ekraniga qaytarish |
| `NOT_OWNER` | Egasi emas | Tugmani umuman ko'rsatmang |
| `OWNER_CANT_LEAVE` | Ega chiqmoqchi | "Room egasi chiqa olmaydi" |
| `SESSION_NOT_LISTENABLE` | Tinglab bo'lmaydi | Tugmani o'chiring |
| `INVALID_ROOM` / `BAD_REQUEST` | So'rov noto'g'ri | Umumiy xato |
| `CENTER_NOT_FOUND` | Markaz yo'q | Bo'limni yashiring |
| `BAD_LOGO` | Rasm qabul qilinmadi | Server matnini ko'rsating |

`room_paired.results[].reason`:
`INVALID_PAIR`, `NOT_A_MEMBER`, `OFFLINE`, `ALREADY_IN_SESSION`,
`ALREADY_PAIRED_IN_THIS_REQUEST`, `CREATE_FAILED`.

**Kanal obunasi:** `channel_subscription_required` eventi room'da ham
avvalgidek keladi. Mavjud modalingizni qayta ishlating.

---

## 10. Qurilishi kerak bo'lgan ekranlar

### O'quvchi uchun

| # | Ekran | Manba |
|---|---|---|
| 1 | Profilda "Mening guruhlarim" | `GET /rooms/mine` → `joined[]` |
| 2 | Roomga kirish (link bosilganda) | `joined_room` yoki `?room=` → `POST /rooms/join` |
| 3 | **Room ekrani** — katta "Speak" tugmasi + guruh holati | `GET /rooms/:id`, WS `room_state` |
| 4 | Suhbat ekrani — "Dars sessiyasi" belgisi + 🎧 indikator | `match_found.room_id`, `listener_joined` |

### O'qituvchi uchun

| # | Ekran | Manba |
|---|---|---|
| 5 | "Mening roomim" + invite link nusxalash | `GET /rooms/mine` → `owned[]` |
| 6 | **Jonli panel** — kim online / qidiryapti / gaplashyapti | WS `room_state` |
| 7 | Jonli suhbatlar ro'yxati + 🎧 tinglash tugmasi | `room_state.active_sessions` |
| 8 | "Raundni boshlash" (tasodifiy) | WS `room_start_round` |
| 9 | "Juftliklarni o'zim tanlayman" | WS `room_pair` |
| 10 | **O'quvchilar jadvali** — bugungi davomat, streak, AI ball | `GET /rooms/:id/students` |
| 11 | Bitta o'quvchi kartasi + AI tarixi | `GET /rooms/:id/students/:user_id` |
| 12 | Davomat grafigi | `GET /rooms/:id/attendance` |
| 13 | Dars jurnali | `GET /rooms/:id/sessions` |
| 14 | Sozlamalar — nom, muzlatish, linkni yangilash, a'zo chiqarish | `PUT /rooms/:id` va h.k. |
| 15 | **Markaz profili** — logo, aloqa, kurslar | `/centers/mine` |

### Barcha foydalanuvchilar

| # | Ekran | Manba |
|---|---|---|
| 16 | Sessiyada aylanuvchi banner | `GET /session-banners` |
| 17 | Markaz kartasi (banner bosilganda) | `GET /centers/:id`, `POST /centers/:id/click` |

### Admin

| # | Ekran | Manba |
|---|---|---|
| 18 | Roomlar CRUD | `/admin/rooms` |
| 19 | **Moderatsiya navbati** (`pending_count` belgisi bilan) | `/admin/centers?status=pending` |

---

## Eslatmalar

- **Room daqiqalari bepul** — kunlik 10 daqiqalik limitga kirmaydi. Lekin
  streak va bosh sahifadagi kunlik progressga **kiradi**.
- **Haftalik reyting**ga room sessiyalari **kirmaydi** (limitsiz daqiqalar
  sovrinli reytingni buzardi).
- **Sessiya limiti room'da 120 daqiqa** — `match_found.limit_minutes` dan oling.
- **Kanal obunasi** room'da ham talab qilinadi.
- Bir foydalanuvchi bir necha roomga a'zo bo'lishi mumkin, lekin
  **bir vaqtda faqat bitta navbatda** turadi.

Batafsil texnik tafsilotlar: [ROOMS.md](ROOMS.md).
Xavfsizlik holati: [XAVFSIZLIK_AUDIT.md](XAVFSIZLIK_AUDIT.md).
