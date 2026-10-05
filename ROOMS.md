# Rooms — o'qituvchi uchun yopiq speaking muhiti

O'quv markazlar bugun Telegramda tartibsiz navbat bilan speaking qilishadi.
Room — o'sha muammoning yechimi: o'qituvchiga yopiq maydon beriladi, o'quvchi
link orqali kiradi, ichida **Speak** bosadi va tizim uni **faqat shu roomdagi**
guruhdoshlaridan biri bilan tasodifiy juftlaydi.

Umumiy navbat (public queue) tegilmagan — u avvalgidek ishlaydi.

---

## 1. Asosiy qarorlar

| Savol | Qaror | Sabab |
|---|---|---|
| Room kim ochadi? | **Faqat platforma admini** (`POST /admin/rooms`) | B2B mahsulot: biz markaz bilan kelishib, room ochib beramiz. Self-service yo'q. |
| Daqiqa hisoblanadimi? | **Yo'q** — kunlik limitga kirmaydi | Markazlar uchun asosiy jozibador taklif. Dars 10 daqiqada to'xtamaydi. |
| Streak-chi? | **Ha, hisoblanadi** | Alohida `room_usage_24h` oynasi bor: limitga tegmaydi, lekin streak va bosh sahifadagi progressni oziqlantiradi. |
| Juftlash | Tasodifiy + oxirgi 3 juftdoshni chetlab o'tadi | Har dars yangi odam bilan gaplashadi. |
| Filtrlar (level/gender) | **Yo'q** | 10-20 kishilik guruhda filtr qo'yilsa hech kim juftlanmay qoladi. |
| Sessiya uzunligi | 120 daqiqa shift | Unutilgan tab kun bo'yi slot band qilmasin. |
| Haftalik reyting | Room sessiyalari **kirmaydi** | Limitsiz daqiqalar sovrinli reytingni buzardi. |
| Kanal obunasi | **Talab qilinadi** (avvalgidek) | Platforma qoidasi, matching sozlamasi emas. |
| O'qituvchi huquqi | O'z roomi ustidan to'liq admin | Nom, muzlatish, link yangilash, chiqarish, tinglash, juftlash. Yangi room ochish yoki limit oshirish — yo'q. |
| Tinglash | **Har doim ko'rinadi** | O'quvchilar ekranida "o'qituvchi tinglayapti" chiqadi. Yashirin rejim yo'q. |

---

## 2. O'quvchi yo'li (3 qadam)

```
1. O'qituvchi linkni tashlaydi:  https://t.me/speakupuzbot?startapp=room_ABC12345
2. O'quvchi bosadi → Mini App ochiladi → /auth/telegram avtomatik roomga qo'shadi
   → javobda  "joined_room": { "id": "...", "name": "..." }
3. Room ekrani → "Speak" tugmasi → WS: room_join_queue → match_found → suhbat
```

Navbat kutish yo'q — Speak bosgan zahoti bo'sh guruhdosh bo'lsa juftlanadi.

**Muqobil link shakli:** `?start=room_ABC12345` (oddiy bot chati). Bot roomga
kirish tugmasini ko'rsatadi, Mini App `?room=ABC12345` bilan ochiladi —
frontend shu paramni o'qib `POST /rooms/join` chaqirishi kerak.

---

## 3. WebSocket kontrakti

Mavjud `/ws?token=<JWT>` sokеtidan foydalanadi. Format o'zgarmagan:
`{"event": "...", "data": {...}}`.

### Client → server

| Event | Data | Izoh |
|---|---|---|
| `room_join_queue` | `{room_id}` | Speak bosildi |
| `room_leave_queue` | `{}` | Bekor qilindi |
| `room_state` | `{room_id}` | Panelni yangilash |
| `room_start_round` | `{room_id}` | **Faqat egasi** — hammani tasodifiy juftlaydi |
| `room_pair` | `{room_id, pairs:[{user1_id,user2_id}]}` | **Faqat egasi** — o'zi tergan juftliklar |
| `room_listen_start` | `{session_id}` | **Faqat egasi** — suhbatni tinglash |
| `room_listen_stop` | `{}` | Tinglashni to'xtatish |
| `listen_offer` | `{session_id, target_id, offer}` | O'quvchi → o'qituvchi |
| `listen_answer` | `{session_id, target_id, answer}` | O'qituvchi → o'quvchi |
| `listen_ice` | `{session_id, target_id, candidate}` | Ikki tomonlama |

### Server → client

| Event | Data | Izoh |
|---|---|---|
| `room_queued` | `{room_id, room_name, queue_size}` | Kutmoqda |
| `room_queue_left` | `{room_id}` | Navbatdan chiqdi |
| `match_found` | public bilan **bir xil** + `room_id`, `room_name` | WebRTC kodi o'zgarmaydi |
| `room_state` | `{room_id, member_count, online_count, queue_size, speaking_count, members[], active_sessions[]}` | Jonli panel |
| `room_round_started` | `{room_id, pairs, leftover}` | Tasodifiy raund natijasi |
| `room_paired` | `{room_id, created, results[]}` | Qo'lda juftlash natijasi, **har juftlik alohida** |
| `listen_started` | `{session_id, room_id, participants[]}` | O'qituvchiga |
| `listen_stopped` | `{session_id, reason}` | O'qituvchiga (`you` \| `session_ended`) |
| `listener_joined` | `{session_id, listener_id, listener_name}` | **O'quvchilarga** |
| `listener_left` | `{session_id, listener_id}` | **O'quvchilarga** |
| `room_error` | `{code, message}` | Quyidagi kodlar |
| `session_ended` | avvalgidek + `room_id` | `room_id != null` → room ekraniga qayting, rate ekraniga emas |

`room_error.code`: `INVALID_ROOM`, `ROOM_NOT_FOUND`, `NOT_A_MEMBER`,
`ROOM_CLOSED`, `NOT_OWNER`, `BAD_REQUEST`, `SESSION_NOT_LISTENABLE`.

`room_paired.results[].reason`: `INVALID_PAIR`, `NOT_A_MEMBER`, `OFFLINE`,
`ALREADY_IN_SESSION`, `ALREADY_PAIRED_IN_THIS_REQUEST`, `CREATE_FAILED`.

`room_state.active_sessions[]` — jonli suhbatlar. O'qituvchining "tinglash"
tugmasi shu ro'yxatdan `session_id` oladi:

```json
{ "session_id": "...", "user1_id": "...", "user1_name": "Ali",
  "user2_id": "...", "user2_name": "Vali", "topic": "...",
  "started_at": "...", "listeners": ["<o'qituvchi id>"] }
```

### `room_state.members[]` elementi

```json
{
  "user_id": "...", "name": "Ali", "photo_url": null, "level": "B1",
  "role": "student",            // "owner" | "student"
  "joined_at": "...",
  "total_sessions": 12, "total_minutes": 84, "last_spoke_at": "...",
  "online": true, "searching": false, "speaking": true
}
```

`room_state` avtomatik push bo'ladi: kimdir navbatga kirsa/chiqsa,
juftlansa, suhbat tugasa yoki uzilib qolsa. O'qituvchi paneli shu bitta
eventga obuna bo'lsa yetarli.

---

## 4. REST API

Hammasi `Authorization: Bearer <JWT>` talab qiladi.

### O'quvchi / o'qituvchi

| Method | Yo'l | Kim | Nima qaytaradi |
|---|---|---|---|
| GET | `/rooms/mine` | har kim | `{owned[], joined[]}` — profil bo'limi uchun |
| GET | `/rooms/preview/:code` | har kim | Kirishdan oldingi ko'rinish |
| POST | `/rooms/join` | har kim | `{code}` → roomga qo'shadi (idempotent) |
| GET | `/rooms/:id` | a'zo | `{room, state}` — room ekranining boshlang'ich yuklamasi |
| GET | `/rooms/:id/members` | a'zo | A'zolar ro'yxati (jonli flaglar bilan) |
| POST | `/rooms/:id/leave` | a'zo | Chiqish (ega chiqa olmaydi) |
| PUT | `/rooms/:id` | ega | Nom / tavsif / `is_active` (muzlatish) |
| POST | `/rooms/:id/regenerate-code` | ega | **Linkni yangilash** |
| DELETE | `/rooms/:id/members/:user_id` | ega | O'quvchini chiqarish (suhbati ham uziladi) |
| GET | `/rooms/:id/sessions?limit=&offset=` | ega | Dars jurnali |
| GET | `/rooms/:id/report?days=7` | ega | Davomat hisoboti |
| GET | `/rooms/:id/students` | ega | **O'quvchilar jadvali** — bugungi davomat, streak, AI ball |
| GET | `/rooms/:id/students/:user_id` | ega | **Bitta o'quvchining to'liq kartasi** |
| GET | `/rooms/:id/attendance?days=14` | ega | **Kun-bakun davomat grafigi** |

`invite_link` va `code` maydonlari **faqat egasiga** qaytariladi — ular
roomning paroli.

`/rooms/:id/report` javobi — har o'quvchi uchun `sessions`, `minutes` va
`days` (necha kun kelgani). Faol bo'lmagan o'quvchilar ham nol bilan
ko'rsatiladi: o'qituvchining birinchi savoli "kim qatnashmadi?" bo'ladi.

### Admin

| Method | Yo'l | Nima |
|---|---|---|
| GET | `/admin/rooms?search=&limit=&offset=` | Ro'yxat (room nomi, ega ismi/username, kod bo'yicha qidiruv) |
| POST | `/admin/rooms` | **Room ochish** |
| PUT | `/admin/rooms/:id` | Tahrirlash / muzlatish / egasini almashtirish |
| DELETE | `/admin/rooms/:id` | O'chirish |
| POST | `/admin/rooms/:id/regenerate-code` | Kodni yangilash (link sizib ketsa) |
| GET | `/admin/rooms/:id/members` | A'zolar |
| GET | `/admin/rooms/:id/sessions` | Sessiyalar |

**Room ochish:**

```json
POST /admin/rooms
{
  "owner_telegram_id": 123456789,   // yoki "owner_id": "<uuid>"
  "name": "IELTS Zone — B2 guruh",
  "description": "Chorshanba/Juma 18:00",
  "max_members": 40,
  "expires_in_days": 90             // 0 yoki yo'q = muddatsiz
}
```

Javobda `invite_link` keladi — o'shani markazga berasiz. Egasi ham uni
o'z profilida (`/rooms/mine` → `owned[]`) ko'radi.

---

## 5. O'qituvchi tinglashi qanday ishlaydi

O'quvchilarning bir-biri bilan aloqasi **tegilmaydi**. Uning ustiga har bir
o'quvchi o'qituvchi tomon **ikkinchi, faqat-yuboruvchi** audio ulanish
ochadi:

```
   O'quvchi A  ⇄  O'quvchi B      ← mavjud 1:1 suhbat, o'zgarmaydi
        ↓              ↓
        └──→ O'qituvchi ←──┘      ← 2 ta yangi sendonly ulanish
```

Mavjud ulanishni qayta kelishuvga (renegotiation) majburlash jonli suhbatni
uzib qo'yishi mumkin edi — parallel ulanish esa unga umuman tegmaydi.
Narxi: har o'quvchidan qo'shimcha ~40 kbit/s yuklash.

**Oqim:**

```
1. O'qituvchi: room_listen_start {session_id}
2. Server → o'quvchilarga: listener_joined     (ekranda indikator chiqadi)
   Server → o'qituvchiga:  listen_started
3. Har o'quvchi yangi RTCPeerConnection (sendonly audio) ochadi
   → listen_offer {session_id, target_id: <o'qituvchi>, offer}
4. O'qituvchi har biriga: listen_answer
5. Ikki tomon: listen_ice
6. Tugatish: room_listen_stop  yoki  suhbat tugashi  yoki  o'qituvchi uzilishi
   → listener_left (o'quvchilarga) + listen_stopped (o'qituvchiga)
```

**Muhim:** o'qituvchi bir vaqtda faqat **bitta** suhbatni tinglay oladi.
Boshqasiga o'tsa, avvalgisi avtomatik uziladi va o'sha o'quvchilarda
indikator o'chadi.

**Yashirin tinglash yo'q — ataylab.** O'quvchilar brauzeri baribir yangi
ulanish ochishi kerak, ya'ni ular texnik jihatdan bilib turadi; buni UI'da
yashirish yolg'on bo'lardi. Bu o'qituvchining sinfda yurib eshitishining
onlayn ekvivalenti, yashirin kuzatuv emas. **Frontendda indikatorni
ko'rsatish shart** — `listener_joined` kelganda ko'rsating, `listener_left`
kelganda o'chiring.

Server tomonda ruxsat qoidasi qattiq: `listen_*` xabari faqat
**ro'yxatdan o'tgan tinglovchi ↔ o'sha suhbat ishtirokchisi** orasida
o'tadi. 16 ta kombinatsiyadan faqat 2 tasi ruxsat etilgan va bu
[test bilan qulflangan](internal/ws/room_listen_events_test.go).

---

## 6. Frontendda qilinadigan ishlar

1. **Profil** — `/rooms/mine`. `owned[]` bo'sh bo'lmasa "Mening roomim" kartasi
   + invite link (copy tugmasi). `joined[]` — "Men a'zo bo'lgan roomlar".
2. **Login** — `/auth/telegram` javobidagi `joined_room` bo'lsa, darrov o'sha
   room ekraniga o'tkazing. Shuningdek URL'dagi `?room=CODE` ni o'qib
   `POST /rooms/join` chaqiring.
3. **Room ekrani** — `GET /rooms/:id` bilan yuklang, keyin `room_state`
   eventiga obuna bo'ling. Katta "Speak" tugmasi → `room_join_queue`.
4. **O'qituvchi paneli** — o'sha `room_state`dan: kim online, kim qidiryapti,
   kim gaplashyapti. Egasi uchun qo'shimcha:
   - "Tasodifiy raund" (`room_start_round`)
   - "Juftliklarni o'zim tanlayman" — a'zolarni tanlab `room_pair`
   - `active_sessions[]` ro'yxati, har birida 🎧 tinglash tugmasi
   - "Linkni yangilash" (`POST /rooms/:id/regenerate-code`)
   - "Roomni muzlatish" (`PUT /rooms/:id` → `is_active:false`)
   - "Hisobot" tabi (`/rooms/:id/report`)
5. **Suhbat ekrani** — `match_found` da `room_id` bo'lsa "Dars sessiyasi"
   badge'i. `listener_joined` kelganda **"🎧 O'qituvchi tinglayapti"**
   indikatori + sendonly PeerConnection ochish. `listener_left` da ikkalasini
   ham yopish.
6. **Admin panel** — rooms CRUD sahifasi.

---

## 7. O'qituvchi o'quvchisi haqida nimani ko'radi

Uchta ekran, uchta savolga javob beradi:

| Endpoint | Savol |
|---|---|
| `/rooms/:id/students` | **Bugun kim keldi, kim kelmadi?** |
| `/rooms/:id/attendance?days=14` | **Guruh umuman faolmi?** |
| `/rooms/:id/students/:user_id` | **Bu o'quvchi rivojlanyaptimi?** |

`/students` javobidagi har qator: bugungi sessiya/daqiqa, room bo'yicha
jami, streak, **AI testlari soni va oxirgi/eng yaxshi band**.

Bitta o'quvchi kartasi (`/students/:user_id`) qo'shimcha: platforma
bo'yicha jami daqiqa, max streak va **AI natijalari tarixi** — har biri
`practice` (oddiy AI check) yoki `mock` (to'liq 3 qismli IELTS), to'rt
mezon bo'yicha ball bilan.

Faoliyat bo'lmagan o'quvchilar ham **nol bilan ko'rsatiladi** — o'qituvchining
birinchi savoli "kim mashq qilmadi", ro'yxatdan tushib qolgan o'quvchi bu
savolga javob bermaydi.

**Transkript ataylab berilmagan.** O'qituvchiga o'rgatish uchun ball va
mezonlar yetarli; o'quvchi aynan nima deganining so'zma-so'z matni —
uning o'zi ulashadigan narsa, o'qituvchi varaqlaydigan ro'yxat emas.

**Oshkoralik:** room ekranida "o'qituvchingiz faoliyatingizni ko'radi"
deb yozilishi kerak. Tinglash indikatori bilan bir xil qoida — hech narsa
yashirin kuzatilmaydi.

### ⚠️ AI test limiti — birinchi kunda uriladi

Hozirgi limit: **bepul foydalanuvchi haftasiga 1 ta AI test**, premium 2 ta.

Ya'ni o'qituvchi "bugun hamma mock topshirsin" desa, o'tgan hafta test
qilgan o'quvchilar **qila olmaydi**. Bu room uchun maxsus sozlanmagan —
sozlash kerak bo'lsa bu pul masalasi (Groq kvotasi), shuning uchun
o'zgartirilmadi. Hozirgi hajm: haftasiga ~48 to'liq test, 19 ta faol
Groq kaliti.

---

## 8. O'quv markaz (StudyCenter) va banner reklamasi

Texnik spetsifikatsiyaning 4.4 va 4.5 bo'limlari. **Markaz** — tashkilot;
**room** — uning guruhi. Bugun markazda odatda bitta guruh bo'ladi, shuning
uchun `rooms.center_id` nullable — lekin ustun birinchi kundan bor, markaz
5 o'qituvchiga o'ssa hech qanday migratsiya kerak emas.

### Kim nima kiritadi

| Nima | Kim | Qayerda |
|---|---|---|
| Markaz nomi, egasi, shartnoma muddati | **SpeakUp admin** | `POST /admin/centers` |
| Banner yoqilganmi (`is_advertised`) | **SpeakUp admin** | `PUT /admin/centers/:id/contract` |
| Logotip, tavsif, kurslar, aloqa | **Markazning o'zi** | `/centers/mine` |
| Tasdiqlash / rad etish | **SpeakUp admin** | `POST /admin/centers/:id/moderate` |

Markaz **o'zini o'zi ro'yxatdan o'tkaza olmaydi** — aks holda banner
xohlagan odam uchun bepul reklamaga aylanardi (spek 4.6).

### Moderatsiya

Markaz yozgan matn va rasm **boshqa foydalanuvchilar ekranida reklama
sifatida** chiqadi, shuning uchun tasdiqsiz efirga chiqmaydi.

Holatlar: `draft` → `pending` → `approved` | `rejected`.

Tahrirlashning ikki turi ataylab ajratilgan:

| Tahrir | Natija | Sabab |
|---|---|---|
| **nom, tavsif, kurslar, logotip** | qayta moderatsiyaga tushadi, banner vaqtincha to'xtaydi | brend da'vosi, narx, erkin matn — aynan shu yerda o'zganing logotipi yoki yolg'on narx paydo bo'ladi |
| **telefon, manzil, ijtimoiy tarmoq** | darhol efirga chiqadi | past xavf, format tekshiriladi, xato faqat markazning o'ziga zarar |

Agar telefon raqamidagi xatoni tuzatish ham bannerni o'chirsa, markazlar
umuman hech narsani yangilamay qo'yadi — shuning uchun shunday.

Faqat **`approved`** holatdagi profil tahrirlanganda `pending`ga qaytadi.
`draft`/`rejected` esa faqat `POST /centers/mine/submit` bosilganda
navbatga tushadi — aks holda yarim to'ldirilgan profil admin navbatiga
tushib, tasdiqlash uchun hech narsa bo'lmasdi.

### Logotip

`POST /centers/mine/logo` (multipart, maydon nomi `logo`).

Bu backend diskka yozadigan yagona foydalanuvchi fayli, shuning uchun
qoidalar qattiq — [center_logo.go](internal/services/center_logo.go):

1. Hajm 2 MB gacha, o'qishdan **oldin** tekshiriladi
2. Avval faqat **sarlavha** o'qiladi (`DecodeConfig`) — 40 KB lik PNG
   50000×50000 deb yozilgan bo'lsa, dekodlashdan oldin rad etiladi
   (dekompressiya bombasi)
3. Kengaytmaga emas, **haqiqiy formatga** ishoniladi — faqat PNG va JPEG
4. Fayl **qayta kodlanadi** — saqlanadigan baytlarni biz yaratamiz, ya'ni
   EXIF, polyglot yoki oxiriga yopishtirilgan hech narsa omon qolmaydi
5. Fayl nomi markaz **UUID**'sidan olinadi, yuklamadan emas — path
   traversal imkonsiz
6. Temp faylga yozilib, keyin `rename` — yarim yozilgan logotip hech qachon
   ko'rsatilmaydi

Saqlash joyi: `UPLOAD_DIR` (default `/data/uploads`), `app.Static` orqali
`/uploads/...` da beriladi. **Docker volume shart** — `speakup_uploads`.
Aks holda har `--build` da barcha logotiplar yo'qoladi.

### Banner

| Method | Yo'l | Kim | Nima |
|---|---|---|---|
| GET | `/session-banners` | har kim | Aylantirish uchun ro'yxat (maks 8 ta) |
| GET | `/centers/:id` | har kim | Banner bosilganda ochiladigan karta |
| POST | `/centers/:id/click` | har kim | Klik hisobi |

Sessiya boshlanganda bir marta chaqiriladi, keyin client o'zi aylantiradi —
server har banner uchun so'rov olmaydi.

**Adolat:** eng kam ko'rsatilganlar oldinga qo'yiladi, so'ng boshi
aralashtiriladi. Sof tasodifiy bo'lsa kichik hamkor omadsizlikdan
yo'qolib ketardi; sof navbat bo'lsa tartib oldindan aniq bo'lardi.

**Hisob:** `banner_impressions` serverda sanaladi (client oshira olmaydi),
`banner_clicks` esa har foydalanuvchi uchun soatiga bir marta —
bitta odamning qayta-qayta bosishi hamkor hisobini shishirmaydi.

Banner chiqishi uchun **beshta shart** — [BannerEligible()](internal/models/study_center.go):
shartnoma amalda, `is_advertised`, `approved`, logotip bor, muddat o'tmagan.
Har biri alohida testda qulflangan.

### Markaz admin endpointlari

| Method | Yo'l | Nima |
|---|---|---|
| GET | `/centers/mine` | Dashboard + guruhlar + `banner_eligible` sababi |
| PUT | `/centers/mine` | Profil tahriri (javobda `needs_review`) |
| POST | `/centers/mine/logo` | Logotip |
| POST | `/centers/mine/submit` | Moderatsiyaga yuborish |

### Admin endpointlari

| Method | Yo'l | Nima |
|---|---|---|
| GET | `/admin/centers?status=pending` | **Moderatsiya navbati** + `pending_count` |
| POST | `/admin/centers` | Markaz ochish (`room_id` bersangiz darhol bog'lanadi) |
| GET | `/admin/centers/:id` | Batafsil + guruhlari |
| POST | `/admin/centers/:id/moderate` | `{approve, note}` — rad etsangiz sabab shart |
| PUT | `/admin/centers/:id/contract` | `is_active`, `is_advertised`, `expires_in_days` |
| PUT | `/admin/rooms/:id/center` | Roomni markazga bog'lash |

---

## 9. Chegaralar va ehtiyot choralari

- **Bir vaqtda bitta navbat.** Room navbatiga kirish umumiy navbatdan
  chiqaradi va aksincha — o'quvchi dars o'rtasida begona odam bilan
  juftlanib qolmaydi.
- **Ikki karra sessiya bo'lmaydi.** `roomMatchMu` + Redis'dan atomar
  "claim" + `CreateSession`dagi `pg_advisory_xact_lock` — public queue'dagi
  bilan bir xil himoya.
- **Room to'lganda** join `ROOM_FULL` qaytaradi; tekshiruv `SELECT ... FOR
  UPDATE` ostida, shuning uchun bir vaqtda kirgan ikki o'quvchi limitdan
  o'tib keta olmaydi.
- **Link sizib ketsa** — `regenerate-code`. Eski linklar darhol ishlamay
  qoladi, mavjud a'zolar esa joyida qoladi.
- **Muddat** — `expires_in_days` bilan room muddatli sotiladi. Muddati
  o'tgan room `is_active=false` kabi: kirish ham, juftlash ham to'xtaydi,
  tarix saqlanadi.
- **Qo'lda juftlashda** har juftlik alohida tekshiriladi: a'zoligi, online
  ekani, allaqachon suhbatda emasligi. Bitta juftlik rad etilsa qolganlari
  baribir yaratiladi — 20 kishilik sinfda bir kishi tabini yopgani butun
  raundni bekor qilmasligi kerak.
- **A'zoni chiqarishda** uning jonli suhbati ham uziladi. Faqat ro'yxatdan
  o'chirish yetarli emasdi: chiqarilgan o'quvchi baribir gaplashib turaverardi.
- **Muzlatishda** navbat ham tozalanadi, aks holda kutib turgan o'quvchilar
  yopiq roomda juftlanib ketardi.
- **Tinglash faqat room sessiyalariga.** Umumiy navbatdagi suhbatlar —
  begona odamlarning shaxsiy suhbati, ularni hech kim tinglay olmaydi.
  Room egasi ham faqat **o'z** roomining suhbatlarini tinglaydi.
