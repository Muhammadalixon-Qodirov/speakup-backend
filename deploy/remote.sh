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

OLD=$(git rev-parse HEAD)
echo "==> Hozirgi commit : $(git rev-parse --short HEAD)"

# --- Kodni yangilash ----------------------------------------------
git fetch --prune origin
git reset --hard "origin/$BRANCH"
NEW=$(git rev-parse HEAD)
echo "==> Yangi commit   : $(git rev-parse --short HEAD)"

if [ "$OLD" = "$NEW" ]; then
  echo "==> O'zgarish yo'q, lekin qayta qurib chiqaman."
fi

# --- Qurish va ko'tarish ------------------------------------------
echo "==> docker compose up -d --build"
docker compose up -d --build

# --- Sog'liqni tekshirish -----------------------------------------
echo "==> Health check ($HEALTH_URL)"
for i in $(seq 1 30); do
  if curl -fsS --max-time 5 "$HEALTH_URL" 2>/dev/null | grep -q '"status":"ok"'; then
    echo "==> SOG'LOM. Deploy muvaffaqiyatli: $(git rev-parse --short HEAD)"
    docker image prune -f >/dev/null 2>&1 || true
    exit 0
  fi
  sleep 3
done

# --- Muvaffaqiyatsiz: orqaga qaytarish ----------------------------
echo "XATO: health check 90 sekundda o'tmadi. Loglar:"
docker logs --tail 60 speakup-api 2>&1 || true

if [ "$OLD" != "$NEW" ]; then
  echo "==> ORQAGA QAYTARAMAN -> $(git rev-parse --short "$OLD")"
  git reset --hard "$OLD"
  docker compose up -d --build
  for i in $(seq 1 20); do
    if curl -fsS --max-time 5 "$HEALTH_URL" 2>/dev/null | grep -q '"status":"ok"'; then
      echo "==> Orqaga qaytarildi, server sog'lom. Yangi kod DEPLOY QILINMADI."
      exit 1
    fi
    sleep 3
  done
  echo "DIQQAT: orqaga qaytarishdan keyin ham sog'lom emas. Qo'lda aralashish kerak!"
fi
exit 1
