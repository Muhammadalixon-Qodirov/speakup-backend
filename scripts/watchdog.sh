#!/bin/bash
# SpeakUp - Health watchdog
#
# Runs from cron every 2 minutes. Hits the Go API's /health and pings
# the admin Telegram chat when something changes state. Idempotent:
# a state file suppresses repeat alerts while the server stays down,
# and the recovery ping only fires once the next successful probe
# comes through.
#
# Important design choices:
#   - Probes BOTH the public URL (through nginx) and 127.0.0.1:8082
#     (direct to the Go container). This separates "nginx is broken"
#     from "Go is broken" and avoids false alarms from nginx hiccups
#     where the API itself is fine.
#   - Retries once before flipping to DOWN. A single 5-second blip is
#     usually a network hiccup, not a real outage.
#   - Clears the stale body file each run so we never report a body
#     left over from a previous successful probe.
#
# Setup (already done, kept for reference):
#   */2 * * * * /root/speakup/scripts/watchdog.sh >> /root/speakup/backups/watchdog.log 2>&1

set -u

PUBLIC_URL="${PUBLIC_URL:-https://api.speak-up.uz/health}"
LOCAL_URL="${LOCAL_URL:-http://127.0.0.1:8082/health}"
STATE_FILE="/tmp/speakup-watchdog.state"
BODY_FILE="/tmp/speakup-watchdog.body"
CURL_MAX_TIME=10
RETRIES=1

# Pull credentials from the API's .env if not already in our env.
ENV_FILE="/root/speakup/.env"
if [ -f "$ENV_FILE" ]; then
    set -a
    # shellcheck disable=SC1090
    . "$ENV_FILE"
    set +a
fi

BOT_TOKEN="${BOT_TOKEN:-}"
CHATS="${ADMIN_ALERT_CHAT_IDS:-}"

now=$(date -u +"%Y-%m-%d %H:%M:%S UTC")

# probe <url>
# Writes body to $BODY_FILE, echoes the HTTP code (or "000" on failure).
probe() {
    local url="$1"
    rm -f "$BODY_FILE"
    local code
    code=$(curl -s -o "$BODY_FILE" -w "%{http_code}" \
        --max-time "$CURL_MAX_TIME" "$url" 2>/dev/null)
    if [ -z "$code" ]; then
        echo "000"
    else
        echo "$code"
    fi
}

probe_with_retry() {
    local url="$1"
    local code
    for i in $(seq 0 "$RETRIES"); do
        code=$(probe "$url")
        if [ "$code" = "200" ]; then
            echo "$code"
            return
        fi
        if [ "$i" -lt "$RETRIES" ]; then
            sleep 2
        fi
    done
    echo "$code"
}

# Probe in this order: local first (Go process), then public (nginx
# path). If local is fine we never even ask about the public path.
LOCAL_CODE=$(probe_with_retry "$LOCAL_URL")
PUBLIC_CODE="n/a"
STATUS="ok"
FAILURE=""

if [ "$LOCAL_CODE" != "200" ]; then
    # Go itself is unhappy - this is the real alert case.
    STATUS="down"
    FAILURE="local"
else
    # Go is healthy. Now verify the public path too, since users hit
    # nginx, not the loopback.
    PUBLIC_CODE=$(probe_with_retry "$PUBLIC_URL")
    if [ "$PUBLIC_CODE" != "200" ]; then
        STATUS="down"
        FAILURE="nginx"
    fi
fi

PREV_STATE="ok"
[ -f "$STATE_FILE" ] && PREV_STATE=$(cat "$STATE_FILE" 2>/dev/null || echo "ok")

send_alert() {
    local title="$1"
    local body="$2"
    if [ -z "$BOT_TOKEN" ] || [ -z "$CHATS" ]; then
        echo "[$now] alert not sent: BOT_TOKEN or ADMIN_ALERT_CHAT_IDS missing"
        return
    fi
    IFS=',' read -ra IDS <<< "$CHATS"
    for chat in "${IDS[@]}"; do
        chat=$(echo "$chat" | xargs)
        [ -z "$chat" ] && continue
        curl -s -o /dev/null \
            -X POST "https://api.telegram.org/bot${BOT_TOKEN}/sendMessage" \
            -d "chat_id=${chat}" \
            --data-urlencode "text=🚨 ${title}

${body}

Time: ${now}" \
            -d "parse_mode=HTML" --max-time 10 || true
    done
}

case "${PREV_STATE}-${STATUS}" in
    ok-down)
        BODY=$(cat "$BODY_FILE" 2>/dev/null | head -c 400)
        [ -z "$BODY" ] && BODY="(no response body)"
        send_alert "speakup-api DOWN (${FAILURE})" \
"Local (Go) HTTP: ${LOCAL_CODE}
Public (nginx) HTTP: ${PUBLIC_CODE}
Failing layer: ${FAILURE}
Body: ${BODY}"
        echo "down" > "$STATE_FILE"
        echo "[$now] DOWN layer=$FAILURE local=$LOCAL_CODE public=$PUBLIC_CODE"
        ;;
    down-ok)
        send_alert "speakup-api recovered" \
"Local (Go) HTTP: ${LOCAL_CODE}
Public (nginx) HTTP: ${PUBLIC_CODE}
Server is responding again."
        echo "ok" > "$STATE_FILE"
        echo "[$now] RECOVERED"
        ;;
    ok-ok)
        echo "ok" > "$STATE_FILE"
        ;;
    down-down)
        echo "[$now] still down layer=$FAILURE local=$LOCAL_CODE public=$PUBLIC_CODE"
        ;;
esac
