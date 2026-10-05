#!/bin/bash
BOT_TOKEN="8703762341:AAEhlFV9-7LQ2RgKq4LknMaRRkf10ZuiKd4"

IDS=$(docker exec speakup-postgres psql -U speakup -d speakup_db -t -A -c "SELECT DISTINCT telegram_id FROM users WHERE telegram_id IS NOT NULL AND telegram_id != 0;")
TOTAL=$(echo "$IDS" | wc -l | tr -d " ")
echo "Jami unique userlar: $TOTAL"

MSG=$(cat <<'EOFMSG'
Salom! 👋

Siz SpeakUp ning dastlabki foydalanuvchilaridan birisiz - bu bizga juda muhim.

Platformamiz ishga tushganiga 48 soat bo'ldi va siz allaqachon uni sinab ko'rgansiz. Shuning uchun aynan sizdan so'ramoqchimiz:

⚡️ Botdan foydalanishda noqulaylik sezdingizmi?
🔍 Kamchilik yoki muammolar yuzaga kelmadimi?
💡 Platformani yaxshilash uchun qanday tavsiyangiz bor?

Fikringizni @speakup_app kanalimizda izohlarda yozing - har bir murojaat ko'rib chiqiladi va keyingi yangilanishga ta'sir qiladi.

Rivojlanishimizga hissa qo'shganingiz uchun oldindan rahmat! 🙏
EOFMSG
)

# Build JSON payload template with python
PAYLOAD_TEMPLATE=$(python3 -c "
import json, sys
msg = sys.stdin.read()
print(json.dumps(msg))
" <<< "$MSG")

SENT=0
FAIL=0
for TG_ID in $IDS; do
  [ -z "$TG_ID" ] && continue
  CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
    "https://api.telegram.org/bot${BOT_TOKEN}/sendMessage" \
    -H "Content-Type: application/json" \
    -d "{\"chat_id\": ${TG_ID}, \"text\": ${PAYLOAD_TEMPLATE}}")
  if [ "$CODE" = "200" ]; then
    SENT=$((SENT + 1))
  else
    FAIL=$((FAIL + 1))
  fi
  if [ $((SENT % 25)) -eq 0 ] && [ $SENT -gt 0 ]; then
    sleep 1
  fi
done

echo "===== NATIJA ====="
echo "Jami userlar: $TOTAL"
echo "Yuborildi: $SENT"
echo "Xato: $FAIL"
