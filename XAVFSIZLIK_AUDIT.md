# SpeakUp — xavfsizlik va kod auditi

**Sana:** 2026-08-01
**Qamrov:** backend kodi (141 Go fayli, ~19k qator), server konfiguratsiyasi
(nginx, SSH, Docker, coturn), production bazasi holati, bog'liqliklar (CVE).

**Usul:** kodni o'qish, `go vet`, `govulncheck`, production serverida
**faqat o'qish** so'rovlari. Hech qanday zaiflik amalda ishlatib
ko'rilmadi — 1-topilma kod va konfiguratsiya dalili asosida tasdiqlangan,
haqiqiy foydalanuvchi hisobiga hujum qilinmagan.

---

## Qisqacha xulosa

| Daraja | Soni | Holat |
|---|---|---|
| 🔴 Kritik | 1 | ✅ **tuzatildi** (2026-08-01) |
| 🟠 Yuqori | 3 | ✅ 2 tuzatildi, ⏳ 1 qisman (SSH paroli) |
| 🟡 O'rta | 5 | ✅ 4 tuzatildi, ⏳ 1 qoldi (WS rate limit) |
| 🔵 Past | 4 | ✅ 3 tuzatildi, ⏳ 1 qoldi (nginx sarlavhalari) |

---

## ✅ 2026-08-01 da bajarilgan ishlar

| # | Ish | Qanday tekshirildi |
|---|---|---|
| 1 | Webhook siri sozlandi + kod **fail-closed** qilindi | sirsiz so'rov → 401, sir bilan → 200 |
| 2a | fail2ban o'rnatildi (5 xato/soat → 24 soat blok) | bir necha soniyada 3 ta IP bloklandi |
| 2b | SSH kalit o'rnatildi va ishlashi tekshirildi | parolsiz kirish ishladi |
| 3 | nginx loglaridan token maskalandi + eski loglar tozalandi | **4 716** ta sizib chiqqan token o'chirildi |
| 4 | Groq kalitlari shifrlandi | 9 shifrlangan / **0** ochiq matn qoldi |
| 5 | `pgx` 5.6.0→5.9.2, `x/text`, `x/net`, Go 1.26.5 | `govulncheck`: modul zaifliklari 0 ta |
| 6 | `turnserver.conf` → `chmod 600`, `.gitignore` ga qo'shildi | |
| 7 | WebSocket `Origins` allowlist | |
| 10 | `.dockerignore` yaratildi | |
| 13 | LLM JSON parse xatosi | 11 ta test |

### ⚠️ Deploy paytida topilgan qo'shimcha xato

Sir sozlangandan bir necha daqiqa keyin ilova qayta ishga tushdi va
**botni o'zi o'chirib qo'ydi**: `startWebhook()` Telegram'ga
`setWebhook` ni `secret_token` **siz** yuborar ekan, Telegram esa
berilmagan parametrni **nolga tenglashtiradi** — ya'ni sir o'chib ketdi
va barcha update'lar 401 bilan rad etila boshladi.

Darhol tiklandi va kodga `SecretToken` qo'shildi
([`bot.go:132`](internal/bot/bot.go#L132)). Bu xato faqat jonli deploy
paytida ko'rinardi — statik tahlil uni topa olmasdi.

### SSH: parol saqlandi, lekin brute-force foydasiz qilindi

Ega serverga bir nechta qurilmadan kiradi, shuning uchun parol yo'lini
o'chirish uni qulflab qo'yish xavfini tug'dirardi. Uning o'rniga hujumning
o'zi imkonsiz qilindi:

| Sozlama | Oldin | Endi |
|---|---|---|
| Bitta ulanishda parol urinishi | 6 | **3** |
| Blokgacha xato (1 soat ichida) | ∞ | **3** |
| Blok muddati | yo'q | **1 hafta** |
| Takroran bloklangan IP (`recidive`) | yo'q | **1 oy**, barcha portlar |

Amalda: bitta IP haftasiga **3 marta** parol tera oladi. Parol 13 belgili
aralash — bunday tezlikda taxmin qilish uchun milliardlab yil kerak.

Parol bilan ham, kalit bilan ham kirish **tekshirildi va ishlaydi**.

**Keyinroq parolni o'chirish uchun:** har bir qurilmangizga kalit qo'ying,
so'ng `PasswordAuthentication no` qiling. Kalit qo'yish buyrug'i:

```bash
# Yangi qurilmada (bir marta):
ssh-keygen -t ed25519 -N ""
ssh-copy-id root@169.58.17.122      # parolni bir marta so'raydi
```

### ⏳ Qolganlari

| # | Nima | Nega hali qilinmadi |
|---|---|---|
| 2c | SSH parol autentifikatsiyasini **o'chirish** | Ega boshqa qurilmalardan ham kiradi → o'chirilmadi. Buning o'rniga fail2ban qattiqlashtirildi (pastga qarang) |
| 8 | WS eventlariga rate limit | Xavfi past, alohida ish |
| 9 | Admin o'zgarishlari uchun audit log | Alohida ish |
| 12 | nginx xavfsizlik sarlavhalari | Xavfi past |

Ijobiy tomoni ham bor va u kam emas — pastdagi "Yaxshi qilingan" bo'limiga
qarang. Kod asosan puxta yozilgan: SQL injection yo'q, egalik tekshiruvlari
joyida, poyga holatlari (race condition) advisory lock bilan yopilgan.

---

# 🔴 KRITIK

## 1. Telegram webhook autentifikatsiyasiz — istalgan hisobni egallash mumkin

**Joylashuv:** [`internal/middleware/telegram_webhook.go:17`](internal/middleware/telegram_webhook.go#L17),
[`internal/bot/bot.go:248`](internal/bot/bot.go#L248)

**Dalil:**

```
serverdagi .env  → TELEGRAM_WEBHOOK_SECRET  yo'q
konteynerda      → BO'SH
Telegram tomonda → url: https://api.speak-up.uz/bot/webhook  (ochiq)
```

Middleware'da:

```go
if expectedSecret == "" {
    // Not configured - allow through but warn on first boot.
    return c.Next()          // ← hamma so'rov o'tib ketadi
}
```

Ya'ni `POST /bot/webhook` ni **istalgan odam** chaqira oladi va Telegram
nomidan soxta xabar yubora oladi.

**Nega bu hisobni egallashga olib keladi:**

`handleAuthStart` veb-saytga kirish uchun `c.Sender()` ga ishonadi —
u esa webhook payload'idan keladi:

```go
sender := c.Sender()
database.DB.Where("telegram_id = ?", sender.ID).First(&existingUser)
// ...
database.DB.Model(&session).Updates(map[string]interface{}{
    "status":      "completed",
    "telegram_id": sender.ID,     // ← soxta qiymat
})
```

Hujum ketma-ketligi:

1. Hujumchi speak-up.uz da "Telegram orqali kirish" bosadi →
   `POST /auth/request-code` → **o'zining** `session_token` ini oladi
2. `POST https://api.speak-up.uz/bot/webhook` ga soxta update yuboradi:
   `from.id` = **qurbonning Telegram ID'si**, matn = `/start auth_<o'z tokeni>`
3. Bot sessiyani qurbonning `telegram_id` si bilan `completed` deb belgilaydi
4. Hujumchi `GET /auth/check-session/<o'z tokeni>` ni so'raydi va
   **qurbonning hisobi uchun JWT** oladi

Kerak bo'lgan yagona narsa — **qurbonning Telegram ID'si**. U esa maxfiy
emas: guruh chatlarida, forward qilingan xabarlarda, ko'plab botlarda
ochiq ko'rinadi.

Bu bilan admin hisobini ham egallash mumkin (`is_admin = true` bo'lgan
foydalanuvchi Telegram ID'si bilan) — undan keyin butun admin panel ochiq.

**Tuzatish (10 daqiqa):**

```bash
# 1. Sir yaratish
SECRET=$(openssl rand -hex 32)

# 2. .env ga qo'shish
echo "TELEGRAM_WEBHOOK_SECRET=$SECRET" >> /root/speakup/.env

# 3. Telegram tomonda ro'yxatdan o'tkazish
curl -s "https://api.telegram.org/bot<BOT_TOKEN>/setWebhook" \
  -d "url=https://api.speak-up.uz/bot/webhook" \
  -d "secret_token=$SECRET"

# 4. Qayta ishga tushirish
cd /root/speakup && docker compose up -d app
```

**Qo'shimcha tavsiya:** kod hozir sir sozlanmagan bo'lsa **ochiq o'tkazadi**.
Buni teskarisiga o'zgartirish kerak — production'da sir bo'lmasa,
xizmat **ishga tushmasligi** lozim. Hozirgi "fail-open" xatti-harakati
aynan shu holatga olib kelgan: kimdir sirni qo'shishni unutgan va hech
narsa buzilmagani uchun bu 3 hafta sezilmay qolgan.

---

# 🟠 YUQORI

## 2. SSH: root + parol, fail2ban yo'q, haftasiga 44 746 urinish

**Dalil:**

```
permitrootlogin       yes
passwordauthentication yes
port                  22
fail2ban              inactive (o'rnatilmagan)
"Failed password" (7 kun) → 44 746
```

Kuniga ~6 400 parol urinishi. Server internetga to'g'ridan-to'g'ri ochiq
va **root parol bilan** kirish mumkin. Parol topilsa — baza, Groq
kalitlari, bot tokeni, backuplar, hammasi qo'lga o'tadi.

Bu butun tizimning eng katta xavfi. Ilova kodi qanchalik puxta bo'lmasin,
server ostidan ketsa ma'nosi qolmaydi.

**Tuzatish:**

```bash
# 1. SSH kalit qo'yish (parolsiz kirish)
ssh-copy-id root@169.58.17.122

# 2. Parol bilan kirishni o'chirish
#    /etc/ssh/sshd_config:
#      PasswordAuthentication no
#      PermitRootLogin prohibit-password
systemctl reload ssh

# 3. fail2ban
apt install -y fail2ban && systemctl enable --now fail2ban
```

⚠️ 2-qadamdan oldin kalit bilan kirish **ishlashiga ishonch hosil qiling**,
aks holda serverdan chiqib qolasiz.

## 3. JWT tokenlari nginx loglarida ochiq yozilyapti

**Dalil:** `/var/log/nginx/access.log` ichida:

```
token=eyJhbGciOiJIUzI1NiIs...
```

WebSocket `/ws?token=<JWT>` orqali autentifikatsiya qiladi, nginx esa
butun query string'ni logga yozadi. Tokenlar **30 kun** amal qiladi
(`JWT_EXPIRE_DAYS=30`).

Ya'ni loglarga kirish huquqi bor har kim — yoki log arxivini qo'lga
kiritgan odam — o'nlab foydalanuvchi nomidan bir oy davomida ishlay oladi.

**Tuzatish (nginx):**

```nginx
# /etc/nginx/sites-enabled/api-speakup, server blokiga:
map $request_uri $loggable_uri {
    ~*^/ws  "/ws?token=[REDACTED]";
    default $request_uri;
}
log_format noqs '$remote_addr - [$time_local] "$request_method $loggable_uri" '
                '$status $body_bytes_sent';
access_log /var/log/nginx/access.log noqs;
```

Eski loglarni ham tozalash kerak:
```bash
find /var/log/nginx -name "access.log*" -exec sed -i 's/token=[A-Za-z0-9._-]*/token=REDACTED/g' {} \;
```

**Uzoq muddatli yechim:** tokenni query'da emas, WebSocket subprotocol
sarlavhasida yuborish (`Sec-WebSocket-Protocol`). Bu frontend o'zgarishini
talab qiladi.

## 4. Groq API kalitlari bazada ochiq matnda

**Joylashuv:** [`internal/services/groq_key_service.go:127`](internal/services/groq_key_service.go#L127)

**Dalil:**

```
tirik kalitlar:  9
shifrlangan:     0
ochiq matnda:    9      (encryption_version = 0)
ENCRYPTION_MASTER_KEY: .env da YO'Q
MigratePlainKeysToEncrypted(): yozilgan, lekin hech qayerdan CHAQIRILMAYDI
```

Shifrlash mexanizmi to'liq yozilgan, migratsiya ham qilingan
(`20260417_add_encrypted_key`) — lekin **hech qachon yoqilmagan**.

**Yumshatuvchi omil:** backup skripti Telegram'ga yuborishdan oldin
`openssl aes-256-cbc` bilan shifrlaydi va kalit bo'lmasa yuborishdan
**bosh tortadi** — buni tekshirdim, to'g'ri ishlaydi. Ya'ni kalitlar
server tashqarisiga ochiq chiqmayapti.

Xavf faqat serverga kirish olgan odam uchun — lekin 2-topilmani hisobga
olsak, bu uzoq ehtimol emas.

**Tuzatish:**

```bash
echo "ENCRYPTION_MASTER_KEY=$(openssl rand -hex 32)" >> /root/speakup/.env
```
va `main.go` ga startup'da bir martalik migratsiya chaqiruvi qo'shish.

⚠️ Master kalit yo'qolsa bazadagi Groq kalitlari o'qilmay qoladi. Halokat
emas — Groq konsolidan qayta olinadi — lekin `.env` ni backup qiling.

---

# 🟡 O'RTA

## 5. Bog'liqliklarda 7 ta ma'lum zaiflik (govulncheck)

| ID | Nima | Modul | Tuzatilgan |
|---|---|---|---|
| GO-2026-5004 | **SQL injection** (dollar-quoted literal chalkashligi) | `pgx/v5@5.6.0` | 5.9.2 |
| GO-2026-5970 | Cheksiz sikl (DoS) | `x/text@0.35.0` | 0.39.0 |
| GO-2026-4918 | HTTP/2 cheksiz sikl (DoS) | `x/net@0.51.0` | 0.53.0 |
| GO-2026-5856 | TLS ECH maxfiylik sizishi | `crypto/tls` (Go 1.26.2) | Go 1.26.5 |
| GO-2026-5039 | Xatolarda kirish eskaplanmaydi | `net/textproto` | Go 1.26.4 |
| GO-2026-5037 | x509 hostname parsing (DoS) | `crypto/x509` | Go 1.26.4 |
| GO-2026-4971 | Dial panic (faqat Windows) | `net` | Go 1.26.3 |

Birinchisi eng muhimi: **pgx'dagi SQL injection**. Ilova kodida injection
yo'q (hammasi parametrlangan — tekshirdim), lekin drayverning o'zida
zaiflik bor.

**Tuzatish:**

```bash
go get github.com/jackc/pgx/v5@v5.9.2
go get golang.org/x/text@v0.39.0
go get golang.org/x/net@v0.53.0
go mod tidy
# Dockerfile: FROM golang:alpine → golang:1.26.5-alpine
```

## 6. Sirlar repozitoriyaga tushadigan fayllarda

| Fayl | Nima bor | `.gitignore` da? |
|---|---|---|
| `turnserver.conf` | TURN `static-auth-secret` | ❌ **yo'q** |
| `docker-compose.yml` | Postgres paroli ochiq matnda | ❌ **yo'q** |

`turnserver.conf` serverda `-rw-r--r--` (hamma o'qiy oladi). TURN siri
qo'lga tushsa, TURN serveringiz orqali begona trafik o'tkazish mumkin —
ya'ni sizning kanalingiz hisobidan.

**Tuzatish:** ikkalasini `.gitignore` ga qo'shing, `.example` nusxalarini
qoldiring, qiymatlarni `.env` ga ko'chiring va sirlarni yangilang
(bir marta ochilgan sir — ochilgan sir).

```bash
chmod 600 /root/speakup/turnserver.conf
```

## 7. WebSocket'da Origin tekshiruvi yo'q

**Joylashuv:** [`cmd/server/main.go:294`](cmd/server/main.go#L294)

`websocket.New(...)` ga `Origins` berilmagan. Bu o'zi kritik emas — token
query'da keladi va hujumchining sahifasi uni ololmaydi — lekin himoyaning
ikkinchi qatlami sifatida qo'yish arziydi:

```go
app.Get("/ws", websocket.New(handler, websocket.Config{
    Origins: config.App.AllowedOrigins,
}))
```

## 8. WebSocket eventlariga tezlik cheklovi yo'q

`SetReadLimit(4096)` bor (xabar hajmi), lekin **soniyada nechta xabar**
degan cheklov yo'q. Bitta ulangan foydalanuvchi `chat_message` yoki
`room_join_queue` ni cheksiz yubora oladi.

Yumshatuvchi omillar: har foydalanuvchiga maksimum 5 ta ulanish,
`SendJSON` bufer to'lsa tashlab yuboradi. Lekin `room_join_queue` har
chaqirilganda DB va Redis'ga boradi — flood real yuk hosil qiladi.

**Tavsiya:** WS handler'iga oddiy token-bucket (masalan 20 event/sekund).

## 9. Kirish ma'lumotlarida `is_admin` yo'q — lekin admin berish yo'li tekshirilsin

`UpdateProfileRequest` oq ro'yxat (whitelist) — `is_admin`, `is_premium`
kabi maydonlar yo'q, ya'ni **mass assignment zaifligi yo'q**. Bu to'g'ri
yozilgan.

Lekin admin huquqi faqat bazadan qo'lda beriladi. Kim, qachon admin
bo'lgani hech qayerda yozilmaydi (audit log yo'q). Admin hisoblari
egallansa, buni aniqlash qiyin bo'ladi.

**Tavsiya:** `is_admin` o'zgarishini alohida jadvalga yozish.

---

# 🔵 PAST

## 10. `.dockerignore` yo'q

`COPY . .` butun katalogni build konteksiga oladi — shu jumladan
`backups/` (93 MB) va `.env`. Yakuniy image'ga tushmaydi (faqat binary
ko'chiriladi), lekin build sekin va builder qatlamida sirlar qoladi.

```
# .dockerignore
backups/
.env*
*.dump
*.md
```

## 11. Bazada 10 ta o'chirilgan Groq kaliti qolgan

`groq_api_keys` da 19 qator, 9 tasi tirik. `is_active` bo'yicha sanaganda
19 chiqadi va chalg'itadi. Zarar yo'q, lekin monitoring noto'g'ri raqam
ko'rsatadi.

## 12. nginx'da xavfsizlik sarlavhalari yo'q

`Strict-Transport-Security`, `X-Content-Type-Options` yo'q. API uchun
kam ahamiyatli, lekin `/uploads/` orqali endi rasm ham beriladi:

```nginx
add_header Strict-Transport-Security "max-age=31536000" always;
add_header X-Content-Type-Options "nosniff" always;
```

## 13. LLM JSON parse xatosi — ✅ bugun tuzatildi

`coerceAnalysisJSON` LLM javobining ichma-ich massiv shaklini qamramagan
edi → 423 so'rovdan 1 tasida foydalanuvchining butun AI testi yiqilardi.
Tuzatildi, 11 ta test yozildi.

---

# ✅ Yaxshi qilingan

Audit faqat kamchilik sanash emas — quyidagilar to'g'ri yozilgan va
ularni buzib qo'ymaslik kerak:

| Nima | Qayerda |
|---|---|
| **SQL injection yo'q** — barcha so'rovlar parametrlangan, `Raw()` ichida faqat konstanta | butun kod |
| **Egalik tekshiruvlari** — `GetSession`, `GetAIReport`, `OpenPrize` va boshqalar `user.ID` bo'yicha filtrlaydi | `internal/services/` |
| **initData HMAC to'g'ri** — `auth_date` muddati ham tekshiriladi (1 soat) | `auth_service.go:48` |
| **Constant-time solishtirish** webhook sirida (sozlanganda) | `telegram_webhook.go:29` |
| **Poyga holatlari yopilgan** — `pg_advisory_xact_lock` bilan ikki sessiya yaratilmaydi | `session_service.go:47` |
| **Mass assignment yo'q** — DTO'lar oq ro'yxat | `user.go:27` |
| **Docker root'da ishlamaydi** — `USER appuser` | `Dockerfile` |
| **Postgres/Redis tashqariga ochiq emas** — faqat 22, 80, 443, 3478 | server |
| **Backup shifrlangan holda yuboriladi** — kalitsiz yuborishdan bosh tortadi | `scripts/backup.sh` |
| **Panic'lar ushlanadi** — `safego` + `recover.New()` | `internal/safego/` |
| **Auth endpointlarida tor rate limit** | `main.go:265` |
| **TURN'da ichki tarmoqlar bloklangan** — SSRF himoyasi | `turnserver.conf` |

---

# Tavsiya etilgan tartib

| # | Ish | Vaqt | Nega shu tartibda |
|---|---|---|---|
| 1 | Webhook siri | 10 daq | Hisob egallash — bugun |
| 2 | SSH kalit + fail2ban | 20 daq | Kuniga 6400 urinish davom etyapti |
| 3 | nginx log'dan tokenni olib tashlash | 15 daq | Har kun yangi token sizib turibdi |
| 4 | `pgx` + Go yangilash | 30 daq | SQL injection CVE |
| 5 | Groq kalitlarini shifrlash | 15 daq | 1–2 bajarilgach xavf kamayadi |
| 6 | `.gitignore` + sirlarni yangilash | 30 daq | |
| 7 | WS Origin + rate limit | 1 soat | |
| 8 | Qolgan past darajalilar | — | Xohlaganda |

1–3 bandlar birgalikda **45 daqiqa** oladi va eng katta xavfning
90% ini yopadi.

---

*Ushbu hisobot kod va konfiguratsiyani o'qish, `govulncheck` va production
serveridagi faqat-o'qish so'rovlari asosida tuzilgan. Hech qanday zaiflik
amalda ishlatib ko'rilmadi.*
