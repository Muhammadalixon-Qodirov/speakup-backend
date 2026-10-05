#!/bin/bash
# SpeakUp - Database Backup Script (encrypted off-site variant)
#
# Cron: 0 3 * * * (kuniga 03:00)
#
# Three layers of safety:
#   1. Local rolling backup in ~/speakup/backups/ (30 days, plain).
#   2. Off-site encrypted copy sent to the admin Telegram chat.
#      AES-256-CBC + PBKDF2 (200k iterations). Key lives in .env as
#      BACKUP_ENCRYPTION_KEY. Without that key the file is useless,
#      so a compromised bot token alone cannot leak user PII.
#   3. Sanity check: if dump < 1KB, abort early (something is wrong).
#
# Decrypt manually:
#   openssl enc -d -aes-256-cbc -pbkdf2 -iter 200000 \
#     -pass pass:$BACKUP_ENCRYPTION_KEY \
#     -in speakup_db_YYYY-MM-DD.dump.enc \
#     -out speakup_db_recovered.dump

set -uo pipefail

BACKUP_DIR="/root/speakup/backups"
DATE=$(date +%Y-%m-%d_%H-%M)
mkdir -p "$BACKUP_DIR"

echo "[$DATE] Starting backup..."

# ---------- 1. PostgreSQL dump ----------
DUMP="$BACKUP_DIR/speakup_db_$DATE.dump"
docker exec speakup-postgres pg_dump -U speakup -d speakup_db --format=custom --file=/tmp/backup.dump
docker cp speakup-postgres:/tmp/backup.dump "$DUMP"
docker exec speakup-postgres rm -f /tmp/backup.dump

DUMP_SIZE=$(stat -c%s "$DUMP" 2>/dev/null || echo 0)
if [ "$DUMP_SIZE" -lt 1024 ]; then
    echo "[$DATE] ERROR: dump suspiciously small ($DUMP_SIZE bytes), aborting"
    exit 1
fi

# ---------- 2. Redis snapshot ----------
docker exec speakup-redis redis-cli BGSAVE >/dev/null 2>&1
sleep 2
docker cp speakup-redis:/data/appendonly.aof "$BACKUP_DIR/redis_$DATE.aof" 2>/dev/null || true
docker cp speakup-redis:/data/dump.rdb "$BACKUP_DIR/redis_$DATE.rdb" 2>/dev/null || true

# ---------- 3. .env snapshot (lokal) ----------
cp /root/speakup/.env "$BACKUP_DIR/env_$DATE.bak"

# ---------- 4. Eski backuplarni o'chirish ----------
find "$BACKUP_DIR" -name "*.dump" -mtime +30 -delete
find "$BACKUP_DIR" -name "*.aof"  -mtime +30 -delete
find "$BACKUP_DIR" -name "*.rdb"  -mtime +30 -delete
find "$BACKUP_DIR" -name "*.bak"  -mtime +30 -delete
find "$BACKUP_DIR" -name "*.enc"  -mtime +30 -delete

# ---------- 5. Encrypted off-site copy via Telegram ----------
# Pull bot token + admin chat + key from the API's .env (single source).
ENV_FILE="/root/speakup/.env"
if [ -f "$ENV_FILE" ]; then
    set -a
    # shellcheck disable=SC1090
    . "$ENV_FILE"
    set +a
fi

send_to_telegram_encrypted() {
    local file="$1"
    local caption="$2"
    [ -z "${BOT_TOKEN:-}" ] && { echo "  skip telegram: BOT_TOKEN missing"; return; }
    [ -z "${ADMIN_ALERT_CHAT_IDS:-}" ] && { echo "  skip telegram: ADMIN_ALERT_CHAT_IDS missing"; return; }
    [ -z "${BACKUP_ENCRYPTION_KEY:-}" ] && { echo "  skip telegram: BACKUP_ENCRYPTION_KEY missing - refusing to send unencrypted PII"; return; }

    # Encrypt the dump in place. Output is binary, save as .enc.
    local enc="${file}.enc"
    openssl enc -aes-256-cbc -pbkdf2 -iter 200000 \
        -pass pass:"${BACKUP_ENCRYPTION_KEY}" \
        -in "$file" -out "$enc"

    if [ ! -s "$enc" ]; then
        echo "  encryption failed, skipping upload"
        return
    fi

    # Use local Telegram Bot API (port 18081) for >50MB uploads;
    # fall back to public api.telegram.org for the 13MB current size.
    local hosts=("http://127.0.0.1:18081" "https://api.telegram.org")

    IFS=',' read -ra IDS <<< "$ADMIN_ALERT_CHAT_IDS"
    for chat in "${IDS[@]}"; do
        chat=$(echo "$chat" | xargs)
        [ -z "$chat" ] && continue

        for host in "${hosts[@]}"; do
            HTTP=$(curl -s -o /tmp/tg_upload_resp -w "%{http_code}" \
                --max-time 180 \
                -F "chat_id=${chat}" \
                -F "caption=${caption}" \
                -F "document=@${enc}" \
                "${host}/bot${BOT_TOKEN}/sendDocument")
            if [ "$HTTP" = "200" ]; then
                echo "  sent to chat=${chat} via ${host}"
                break
            else
                echo "  failed (HTTP $HTTP) via ${host}: $(head -c 200 /tmp/tg_upload_resp 2>/dev/null)"
            fi
        done
    done
    rm -f /tmp/tg_upload_resp
}

ENC_SIZE=$(du -h "$DUMP" | cut -f1)
CAPTION="🔐 SpeakUp DB backup (AES-256 encrypted)
📅 ${DATE}
📦 ${ENC_SIZE}
🖥 $(hostname)
🔑 Decrypt key: BACKUP_ENCRYPTION_KEY in .env"
send_to_telegram_encrypted "$DUMP" "$CAPTION"

# ---------- 6. Hisobot ----------
SIZE=$(du -sh "$BACKUP_DIR" 2>/dev/null | cut -f1)
COUNT=$(ls "$BACKUP_DIR"/*.dump 2>/dev/null | wc -l)

echo "[$DATE] Backup done! $COUNT backups, total: $SIZE"
echo "[$DATE] File: $DUMP"
