#!/bin/bash
# SpeakUp - Boshqa serverga ko'chirish
# 
# QANDAY ISHLATILADI:
# 1. Yangi serverda Docker o'rnating
# 2. Shu loyiha papkasini yangi serverga ko'chiring:
#    scp -r /home/einvestment/speakup/ user@NEW_SERVER:/home/user/speakup/
# 3. Oxirgi backupni yangi serverga ko'chiring:
#    scp backups/speakup_db_LATEST.dump user@NEW_SERVER:/home/user/speakup/backups/
# 4. Yangi serverda shu scriptni ishga tushiring

BACKUP_FILE="$1"

if [ -z "$BACKUP_FILE" ]; then
  echo "Usage: ./migrate-to-new-server.sh /path/to/speakup_db_XXXX.dump"
  exit 1
fi

echo "=== 1. Starting containers ==="
cd /home/einvestment/speakup
docker compose up -d postgres redis
sleep 10

echo "=== 2. Restoring database ==="
docker cp "$BACKUP_FILE" speakup-postgres:/tmp/restore.dump
docker exec speakup-postgres pg_restore -U speakup -d speakup_db --clean --if-exists /tmp/restore.dump 2>&1

echo "=== 3. Starting app ==="
docker compose up -d app
sleep 5

echo "=== 4. Health check ==="
curl -s http://localhost:8082/health

echo ""
echo "=== 5. User count ==="
docker exec speakup-postgres psql -U speakup -d speakup_db -c "SELECT count(*) as users FROM users;"

echo ""
echo "DONE! DNS ni yangi server IP ga o'zgartiring."
echo "Nginx + SSL ni yangi serverda sozlang."
