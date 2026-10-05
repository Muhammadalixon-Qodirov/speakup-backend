#!/usr/bin/env bash
set -uo pipefail
cd ~/speakup
exec 9>~/speakup/backups/.bcast.lock
flock -n 9 || { echo "BUSY"; exit 1; }
BOT_TOKEN=$(grep -E '^BOT_TOKEN=' .env | cut -d= -f2-)
L=/home/einvestment/speakup/backups/redo_send_20260601_233154.log
OUT=/home/einvestment/speakup/backups/retry_$(date +%H%M%S).log
read -r -d '' MSG << 'MEOF' || true
👇 Ushbu havolaga kirib, like bosing! ❤️

https://www.instagram.com/reel/DZDE9ZEtIn8/?utm_source=ig_web_copy_link&igsh=NTc4MTIwNjQ2YQ==
MEOF
# transient = FAIL lines with empty description
mapfile -t IDS < <(grep "^FAIL" "$L" | awk '$4=="::" && $5=="" {print $2}')
# fallback: any FAIL whose tail after :: is empty
if [ ${#IDS[@]} -eq 0 ]; then
  mapfile -t IDS < <(awk -F" :: " '/^FAIL/ && $2=="" {split($1,a," "); print a[2]}' "$L")
fi
ok=0; bad=0
for id in "${IDS[@]}"; do
  [ -z "$id" ] && continue
  r=$(curl -s --max-time 25 -G "https://api.telegram.org/bot${BOT_TOKEN}/sendMessage" \
      --data-urlencode "chat_id=${id}" --data-urlencode "text=${MSG}" --data-urlencode "disable_web_page_preview=false")
  if printf '%s' "$r" | grep -q '"ok":true'; then
    mid=$(printf '%s' "$r" | grep -o '"message_id":[0-9]*' | head -1 | grep -o '[0-9]*')
    echo "$id $mid" >> "$OUT"; ok=$((ok+1))
  else
    d=$(printf '%s' "$r" | grep -o '"description":"[^"]*"' | head -1)
    echo "FAIL $id :: $d" >> "$OUT"; bad=$((bad+1))
  fi
  sleep 0.3
done
echo "RETRY_DONE targets=${#IDS[@]} ok=$ok bad=$bad log=$OUT"
