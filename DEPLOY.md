# Deploy — qanday ishlaydi

## Qisqa javob

```
lokalda o'zgartirdingiz  ->  git push  ->  tayyor
```

`main` branch'ga push qilsangiz GitHub Actions o'zi:

1. Kodni tekshiradi (`go build`, `go vet`, `go test`) — **buzuq kod prodga chiqmaydi**
2. Serverga SSH orqali kiradi
3. `git pull` + `docker compose up -d --build`
4. `/health` ni tekshiradi
5. Sog'lom bo'lmasa — **avtomatik orqaga qaytaradi** va push qilingan kod prodda qolmaydi

## Kundalik ish

```bash
cd ~/speakup
# ... fayllarni tahrirlaysiz ...
git add -A
git commit -m "nima o'zgardi"
git push
```

Keyin GitHub'dagi **Actions** sahifasida jarayonni kuzatasiz. ~2-4 daqiqa.

## Muhim qoidalar

**`.env` hech qachon repoga tushmaydi.** Sirlar faqat serverda: `/root/speakup/.env`.
Yangi o'zgaruvchi qo'shsangiz:

1. `.env.example` ga nomini yozasiz (qiymatsiz) va commit qilasiz
2. Serverdagi `.env` ga haqiqiy qiymatni **qo'lda** qo'shasiz
3. Keyin push qilasiz

Agar teskari tartibda qilsangiz, deploy `.env` da kalit yo'qligidan yiqiladi.

**`docker-compose.yml` endi repoda.** Ilgari u `.gitignore` da edi, chunki ichida
Postgres paroli qattiq yozilgan edi. Endi parol `.env` dan olinadi:

```yaml
POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:?.env da POSTGRES_PASSWORD aniqlanmagan}
```

**`turnserver.conf` repoda yo'q** — ichida `static-auth-secret` bor, serverda qoladi.

## Qo'lda deploy (Actions ishlamasa)

```bash
ssh root@169.58.17.122 'bash -s' < deploy/remote.sh
```

Bu Actions bajaradigan aynan o'sha skript — orqaga qaytarish ham ishlaydi.

## Birinchi sozlash (bir marta)

```bash
ssh root@169.58.17.122 'bash -s' < deploy/server-setup.sh
```

Skript nima qilishini aytib boradi va oxirida GitHub'da qo'shilishi kerak
bo'lgan deploy key va secret'larni ko'rsatadi.

## Kerakli GitHub secret'lar

| Secret | Qiymat |
|---|---|
| `DEPLOY_HOST` | `169.58.17.122` |
| `DEPLOY_USER` | `root` |
| `DEPLOY_KNOWN_HOSTS` | serverning SSH host kaliti (`server-setup.sh` chiqaradi) |
| `DEPLOY_SSH_KEY` | `/root/.ssh/id_gh_actions` ning to'liq matni |

Bundan tashqari repo **Deploy keys** ichiga `/root/.ssh/id_speakup_deploy.pub`
qo'shiladi — server private reponi `git pull` qilishi uchun. Yozish huquqi
berilmaydi, faqat o'qish.

## Server haqida

| | |
|---|---|
| IP | `169.58.17.122` (Ubuntu 24.04) |
| Papka | `/root/speakup` |
| API | `https://api.speak-up.uz` -> `127.0.0.1:8082` (nginx) |
| Konteynerlar | `speakup-api`, `speakup-postgres`, `speakup-redis`, `speakup-coturn` |
| Health | `https://api.speak-up.uz/health` |

`speakup-coturn` host tarmog'ida ishlaydi va `turnserver.conf` ga bog'liq —
uni deploy o'zgartirmaydi.

## Loyiha tarkibi — qaysi repo nima

SpeakUp ikki qismdan iborat va ular **alohida** repolarda:

| Qism | Repo | Texnologiya |
|---|---|---|
| Backend (bu repo) | `Muhammadxon2oo7/speakup-backend` | Go 1.26, Docker |
| Frontend | `Muhammadxon2oo7/speak-up` | Next.js 14 + React 18 |

`FRONTEND.md` ikkala repoda ham bor va bir xil — frontend bilan backend
o'rtasidagi API shartnomasi shu faylda. **O'zgartirsangiz ikkalasida ham
yangilang**, aks holda ular bir-biridan uzilib qoladi.

Diqqat: `Muhammadxon2oo7/backend` — 2025-yilning eski mock serveri
(`db.json`, `server.js`). Bu loyihaga aloqasi yo'q, chalkashtirmang.
