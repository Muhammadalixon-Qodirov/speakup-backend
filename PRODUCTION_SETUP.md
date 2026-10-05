# SpeakUp - Production Setup

What needs to be in place before the first public users log in.

## 1. Environment variables (server `.env`)

Add these to `/home/einvestment/speakup/.env` if they're not already there:

```bash
# Required for ALL the new safety nets
APP_ENV=production

# REQUIRED for admin alerts & watchdog: comma-separated Telegram chat IDs
# that should receive panic notifications, watchdog DOWN/RECOVERED pings,
# and boot pings. Get your chat ID by messaging @userinfobot.
ADMIN_ALERT_CHAT_IDS=6407499097

# Existing - keep them
BOT_TOKEN=...
DATABASE_URL=...
REDIS_URL=...
JWT_SECRET_KEY=...
ALLOWED_ORIGINS=https://speakup.uzbekhub.uz
MINI_APP_URL=https://speakup.uzbekhub.uz
WEBHOOK_URL=https://speakup.uzbekhub.uz/bot/webhook
```

After editing `.env`, restart:

```bash
cd /home/einvestment/speakup
docker compose up -d app
```

You should see a `🚨 speakup-api boot` message in the admin chat
within a few seconds. If you don't, the chat ID is wrong or the bot
hasn't been DM'd yet (you must send `/start` to the bot at least once
from each admin account before it can message you).

## 2. Cron jobs

```bash
crontab -e
```

Add:

```cron
# Daily backup at 03:30 UTC (08:30 Tashkent)
30 3 * * *  /home/einvestment/speakup/scripts/backup.sh >> /var/log/speakup-backup.log 2>&1

# Watchdog: hit /health every 2 min, alert admin if down
*/2 * * * *  /home/einvestment/speakup/scripts/watchdog.sh >> /var/log/speakup-watchdog.log 2>&1
```

Make sure both scripts are executable:

```bash
chmod +x /home/einvestment/speakup/scripts/backup.sh
chmod +x /home/einvestment/speakup/scripts/watchdog.sh
```

## 3. Test backup restore (do this BEFORE launch)

```bash
# Take a fresh backup
/home/einvestment/speakup/scripts/backup.sh

# Pick the newest dump
LATEST=$(ls -t /home/einvestment/speakup/backups/speakup_db_*.dump | head -1)

# Restore into a throwaway temp DB (does NOT touch production)
docker exec speakup-postgres psql -U speakup -d postgres -c "DROP DATABASE IF EXISTS speakup_test;"
docker exec speakup-postgres psql -U speakup -d postgres -c "CREATE DATABASE speakup_test;"
docker cp "$LATEST" speakup-postgres:/tmp/restore.dump
docker exec speakup-postgres pg_restore -U speakup -d speakup_test /tmp/restore.dump

# Verify counts match
docker exec speakup-postgres psql -U speakup -d speakup_test -c "SELECT COUNT(*) FROM users;"
docker exec speakup-postgres psql -U speakup -d speakup_db   -c "SELECT COUNT(*) FROM users;"

# Drop test DB
docker exec speakup-postgres psql -U speakup -d postgres -c "DROP DATABASE speakup_test;"
```

If counts match → backups are good and restorable.

## 4. Watchdog smoke test

```bash
# Manually run the watchdog once to make sure it can reach Telegram
/home/einvestment/speakup/scripts/watchdog.sh
# Then break the API briefly to test alerting
docker compose stop app
sleep 30  # wait for the next cron tick OR just run watchdog manually
/home/einvestment/speakup/scripts/watchdog.sh   # should DM you "speakup-api DOWN"
docker compose start app
sleep 30
/home/einvestment/speakup/scripts/watchdog.sh   # should DM "recovered"
```

## 5. Rollback plan

If something blows up after deploy:

```bash
cd /home/einvestment/speakup
docker compose down
# Restore latest backup
LATEST=$(ls -t backups/speakup_db_*.dump | head -1)
docker compose up -d postgres redis
docker exec speakup-postgres psql -U speakup -d postgres -c "DROP DATABASE speakup_db;"
docker exec speakup-postgres psql -U speakup -d postgres -c "CREATE DATABASE speakup_db;"
docker cp "$LATEST" speakup-postgres:/tmp/restore.dump
docker exec speakup-postgres pg_restore -U speakup -d speakup_db /tmp/restore.dump
# Roll back the docker image (if you tagged the previous build)
docker compose up -d app
```

## 6. What's protected now (post-hardening)

- ✅ Every background goroutine wrapped in `safego.Go` - panics never crash the server, they get logged + DM'd
- ✅ `/health` endpoint returns 503 if Postgres or Redis are down (watchdog + UptimeRobot-friendly)
- ✅ Graceful shutdown closes WS, drains HTTP, then closes Postgres + Redis
- ✅ Hub supports up to 5 simultaneous connections per user with oldest-first eviction
- ✅ Bot.Send wrapped with retry/backoff that respects Telegram 429 `retry_after`
- ✅ Per-endpoint rate limits on auth, OpenPrize, GrantPremium, CreateFeedback
- ✅ GrantPremium runs in a transaction - premium update + coupon burn either both commit or both roll back
- ✅ Rating UPDATE uses NUMERIC for precision and CASE for the first-rating edge case
- ✅ Leaderboard cached in Redis (60s) with a hard 2000-user scan ceiling
- ✅ Daily auto-backup with 30-day retention
- ✅ Front-end error boundary (`app/error.tsx`, `app/(main)/error.tsx`) - no white screens
- ✅ Watchdog alerts admin chat when the API goes down or comes back

## 7. What's still NOT protected (read this!)

- ❌ **No replicas** - single Postgres, single Redis, single API process. Hardware failure = downtime
- ❌ **No CDN / DDoS protection** - Cloudflare in front of speakup.uzbekhub.uz would help (free tier is enough)
- ❌ **TURN server (coturn) capacity untested** - single box probably handles ~200 concurrent voice sessions before audio quality degrades
- ❌ **No load test ever performed** - real traffic is the first stress test
- ❌ **No structured error tracking** (Sentry/Rollbar) - only Telegram alerts. Set up Sentry when you have time

## 8. Recommended launch sequence

1. Set `ADMIN_ALERT_CHAT_IDS` and confirm boot ping arrives
2. Install both cron jobs
3. Run the backup/restore test from §3
4. Run the watchdog test from §4
5. Invite 30-50 trusted users (closed beta)
6. Watch `docker logs -f speakup-api` and the admin alert chat for 2-3 days
7. If no critical alerts → soft launch to wider audience
8. Keep `docker stats speakup-api` up in a tmux pane for the first week to spot CPU/memory creep

## 9. Useful commands cheat sheet

```bash
# Tail API logs
docker compose logs -f app

# Live resource usage
docker stats speakup-api speakup-postgres speakup-redis

# Hit health
curl https://speakup.uzbekhub.uz/health

# DB shell
docker exec -it speakup-postgres psql -U speakup -d speakup_db

# Force a backup right now
/home/einvestment/speakup/scripts/backup.sh

# Force a watchdog run right now
/home/einvestment/speakup/scripts/watchdog.sh

# Restart just the app (keeps DB/Redis)
docker compose up -d app
```
