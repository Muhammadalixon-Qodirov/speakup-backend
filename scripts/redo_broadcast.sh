#!/usr/bin/env bash
set -uo pipefail
cd ~/speakup
# Single-instance guard: refuse to run if another copy holds the lock.
exec 9>~/speakup/backups/.bcast.lock
if ! flock -n 9; then echo "ANOTHER INSTANCE RUNNING — exiting"; exit 1; fi

BOT_TOKEN=$(grep -E '^BOT_TOKEN=' .env | cut -d= -f2-)
TS=$(date +%Y%m%d_%H%M%S)
DELLOG=~/speakup/backups/redo_delete_${TS}.log
LOG=~/speakup/backups/redo_send_${TS}.log
RESULT=~/speakup/backups/redo_${TS}_summary.txt
ARCH=~/speakup/backups/old_promo_logs
mkdir -p "$ARCH"

read -r -d '' MSG << 'MEOF' || true
👇 Ushbu havolaga kirib, like bosing! ❤️

https://www.instagram.com/reel/DZDE9ZEtIn8/?utm_source=ig_web_copy_link&igsh=NTc4MTIwNjQ2YQ==
MEOF

# ---- 1. DELETE every previously-sent message (all old logs) ----
deleted=0; delfail=0
shopt -s nullglob
for f in ~/speakup/backups/promo_bcast_*.log; do
  while read -r chat msg _; do
    [ "$chat" = "FAIL" ] && continue
    [ -z "${msg:-}" ] && continue
    r=$(curl -s --max-time 15 -G "https://api.telegram.org/bot${BOT_TOKEN}/deleteMessage" \
        --data-urlencode "chat_id=${chat}" --data-urlencode "message_id=${msg}")
    if printf '%s' "$r" | grep -q '"ok":true'; then deleted=$((deleted+1)); else delfail=$((delfail+1)); echo "$chat $msg :: $r" >>"$DELLOG"; fi
  done < "$f"
  mv "$f" "$ARCH"/ 2>/dev/null
done
echo "deleted=$deleted delfail=$delfail" | tee "$RESULT"

# ---- 2. FRESH send to all eligible, exactly once ----
mapfile -t IDS < <(docker exec speakup-postgres psql -U speakup -d speakup_db -tA -c \
"SELECT telegram_id FROM users WHERE telegram_id != 0 AND is_banned = false AND is_active = true AND bot_blocked = false AND deleted_at IS NULL ORDER BY id;")
total=${#IDS[@]}
sent=0; failed=0; i=0
echo "Recipients: $total | send started: $(date)" | tee -a "$RESULT"
send_one() {
  curl -s --max-time 25 -G "https://api.telegram.org/bot${BOT_TOKEN}/sendMessage" \
    --data-urlencode "chat_id=$1" --data-urlencode "text=${MSG}" --data-urlencode "disable_web_page_preview=false"
}
for id in "${IDS[@]}"; do
  [ -z "$id" ] && continue
  resp=$(send_one "$id")
  if printf '%s' "$resp" | grep -q '"ok":true'; then
    mid=$(printf '%s' "$resp" | grep -o '"message_id":[0-9]*' | head -1 | grep -o '[0-9]*')
    echo "$id $mid" >> "$LOG"; sent=$((sent+1))
  else
    ra=$(printf '%s' "$resp" | grep -o '"retry_after":[0-9]*' | head -1 | grep -o '[0-9]*')
    if [ -n "${ra:-}" ] && [ "$ra" -gt 0 ] 2>/dev/null; then
      sleep "$ra"; resp=$(send_one "$id")
      if printf '%s' "$resp" | grep -q '"ok":true'; then
        mid=$(printf '%s' "$resp" | grep -o '"message_id":[0-9]*' | head -1 | grep -o '[0-9]*')
        echo "$id $mid" >> "$LOG"; sent=$((sent+1)); i=$((i+1)); continue
      fi
    fi
    desc=$(printf '%s' "$resp" | grep -o '"description":"[^"]*"' | head -1)
    echo "FAIL $id :: $desc" >> "$LOG"; failed=$((failed+1))
  fi
  i=$((i+1))
  if [ $((i % 25)) -eq 0 ]; then sleep 1; fi
done
{
  echo "send finished: $(date)"
  echo "sent=$sent failed=$failed total_targets=$total"
  echo "delete_phase: deleted=$deleted delfail=$delfail"
  echo "send_log=$LOG"
  echo "DONE_MARKER"
} | tee -a "$RESULT"
