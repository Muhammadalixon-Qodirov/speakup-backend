#!/usr/bin/env bash
# ======================================================================
#  Serverda ishlaydigan deploy skripti. GitHub Actions SSH orqali
#  buni ishga tushiradi. Qo'lda ham ishlatish mumkin:
#      ssh root@SERVER 'bash -s' < deploy/remote.sh
# ======================================================================
set -euo pipefail

APP_DIR=/root/speakup
HEALTH_URL=http://127.0.0.1:8082/health
BRANCH=main

cd "$APP_DIR"

# Papka egasi ubuntu, biz root — gitni to'xtatmaslik uchun
git config --global --get-all safe.directory 2>/dev/null | grep -qx "$APP_DIR" || git config --global --add safe.directory "$APP_DIR"

# --- Oldindan tekshirish: sirlar joyidami? -------------------------
[ -f .env ] || { echo "XATO: $APP_DIR/.env topilmadi. Deploy to'xtatildi."; exit 1; }
grep -q '^POSTGRES_PASSWORD=' .env || {
  echo "XATO: .env da POSTGRES_PASSWORD yo'q."
  echo "docker-compose.yml shuni talab qiladi. DATABASE_URL ichidagi parol bilan"
  echo "bir xil qiymatni .env ga qo'shing, keyin qaytadan urinib ko'ring."
  exit 1
}

# Birinchi deployda serverda hali commit yo'q (unborn HEAD) — rev-parse
# xato beradi va set -e skriptni to'xtatadi. Shuning uchun bo'sh qoldiramiz.
OLD=$(git rev-parse HEAD 2>/dev/null || true)
if [ -n "$OLD" ]; then
  echo "==> Hozirgi commit : $(git rev-parse --short HEAD)"
else
  echo "==> Hozirgi commit : yo'q — bu birinchi deploy"
fi

# --- Kodni yangilash ----------------------------------------------
git fetch --prune origin
git reset --hard "origin/$BRANCH"
NEW=$(git rev-parse HEAD)
echo "==> Yangi commit   : $(git rev-parse --short HEAD)"

if [ "$OLD" = "$NEW" ]; then
  echo "==> O'zgarish yo'q, lekin qayta qurib chiqaman."
fi

# --- Orqaga qaytarish funksiyasi ----------------------------------
# Alohida funksiya, chunki ikki xil nosozlikda kerak bo'ladi: qurish
# yiqilganda va health check o'tmaganda.
orqaga_qaytar() {
  if [ -z "$OLD" ] || [ "$OLD" = "$NEW" ]; then
    echo "DIQQAT: qaytariladigan oldingi commit yo'q. Qo'lda aralashish kerak!"
    return
  fi
  echo "==> ORQAGA QAYTARAMAN -> $(git rev-parse --short "$OLD")"
  git reset --hard "$OLD"
  if ! docker compose up -d --build; then
    echo "DIQQAT: orqaga qaytarishda ham qurish yiqildi. Qo'lda aralashish kerak!"
    return
  fi
  for i in $(seq 1 20); do
    if curl -fsS --max-time 5 "$HEALTH_URL" 2>/dev/null | grep -q '"status":"ok"'; then
      echo "==> Orqaga qaytarildi, server sog'lom. Yangi kod DEPLOY QILINMADI."
      return
    fi
    sleep 3
  done
  echo "DIQQAT: orqaga qaytarishdan keyin ham sog'lom emas. Qo'lda aralashish kerak!"
}

# --- Qurish va ko'tarish ------------------------------------------
# set -e ni ataylab chetlab o'tamiz: qurish yiqilsa skript shu yerda
# to'xtab qolsa, git allaqachon yangi commit'ga o'tgan, konteynerlar esa
# eski bo'lib qoladi - ya'ni server nomuvofiq holatda qoladi va orqaga
# qaytarish bajarilmaydi. Shuning uchun natijani o'zimiz tekshiramiz.
echo "==> docker compose up -d --build"
if ! docker compose up -d --build; then
  echo "XATO: qurish yoki ko'tarish yiqildi."
  orqaga_qaytar
  exit 1
fi

# --- Sog'liqni tekshirish -----------------------------------------
echo "==> Health check ($HEALTH_URL)"
for i in $(seq 1 30); do
  if curl -fsS --max-time 5 "$HEALTH_URL" 2>/dev/null | grep -q '"status":"ok"'; then
    echo "==> SOG'LOM. Deploy muvaffaqiyatli: $(git rev-parse --short HEAD)"
    # Sidecar holati deploy natijasiga ta'sir qilmaydi: u birinchi marta
    # ~1.5 GB model yuklaydi va bir necha daqiqa "loading" bo'lib turadi.
    # Talaffuz endpointlari shu orada "ishlamayapti" deb javob beradi,
    # qolgan API esa normal ishlaydi.
    echo "--- sidecar holati (ma'lumot uchun) ---"
    docker compose ps talaffuz 2>/dev/null || true
    docker image prune -f >/dev/null 2>&1 || true
    exit 0
  fi
  sleep 3
done

# --- Muvaffaqiyatsiz: orqaga qaytarish ----------------------------
echo "XATO: health check 90 sekundda o'tmadi. Loglar:"
docker logs --tail 60 speakup-api 2>&1 || true
orqaga_qaytar
exit 1
