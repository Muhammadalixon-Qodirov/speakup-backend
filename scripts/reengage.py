#!/usr/bin/env python3
# Re-engagement broadcast for SpeakUp. Sends one of several rotating,
# personalised messages to dormant users with an inline "open the app"
# button. Logs (telegram_id, message_id, variant) so a companion undo
# can delete the whole wave within Telegram's 48h window.
#
# Usage:
#   python3 reengage.py test                 -> send ALL variants to admin only
#   python3 reengage.py live 3d|7d|14d|30d|all
import json, os, sys, time, subprocess, datetime

HERE = os.path.expanduser("~/speakup")
MINIAPP = "https://speak-up.uz"
BTN = "🎤 SpeakUp'ni ochish"
# This host's DNS for api.telegram.org returns an unreachable IPv6 / a
# blocked IP, so pin every call to a verified-reachable Telegram IPv4
# with correct SNI via curl --resolve (same trick as the container pin).
TG_IP = "149.154.167.220"

def env(key):
    with open(os.path.join(HERE, ".env")) as f:
        for line in f:
            if line.startswith(key + "="):
                return line.split("=", 1)[1].strip()
    return ""

BOT_TOKEN = env("BOT_TOKEN")
ADMIN_ID = env("ADMIN_ALERT_CHAT_IDS").split(",")[0].strip()
API = "https://api.telegram.org/bot%s/" % BOT_TOKEN

# {name} is replaced with the user's first name (fallback: "do'stim").
VARIANTS = [
    # 1 — AI "report", humour
    "🤖 SpeakUp AI hisobot berdi: «{name} yo'qoldi» 😂\n\n"
    "Jiddiy gap: speaking skill'da \"pause\" tugmasi yo'q — har kuni biroz unutilib boradi. "
    "Bor-yo'g'i 1 ta mashq, 3 daqiqa. Qaytib kelmaysanmi?",

    # 2 — "later never comes"
    "Ingliz tilini \"keyinroq\" o'rganaman deb o'ylayapsanmi? 😏\n\n"
    "\"Keyinroq\" hech qachon kelmasligini ikkalamiz ham bilamiz. "
    "Hozir 1 ta mashq — atigi 3 daqiqa. Qo'rqmaysanmi? 👀",

    # 3 — miss you / long time
    "Voy, ancha bo'ldi-ya... 🥲\n\n"
    "{name}, anchadan beri ko'rishmadik. SpeakUp yangilandi, yangi mashqlar qo'shildi. "
    "Bitta \"Hello\" aytib ko'rmaysanmi? Biz hali ham shu yerdamiz 💙",

    # 4 — streak / momentum
    "Streak'ing o'chib qoldi 🔥➡️💨\n\n"
    "{name}, lekin hammasini bugun qaytadan boshlash mumkin. 1 ta speaking — va yana yo'lga tushasan. "
    "3 daqiqa vaqting bormi?",

    # 5 — FOMO / social
    "Hozir kimdir sen bilan suhbatlashishni kutyapti 👀\n\n"
    "Real odamlar bilan jonli speaking — bir tugma narida. {name}, kirib 3 daqiqa gaplashamizmi?",

    # 6 — direct / warm
    "{name}, ingliz tiling seni sog'indi 😄\n\n"
    "1 mashq = 3 daqiqa = bir qadam oldinga. Hozir boshlaymizmi?",
]

def fallback_name(n):
    n = (n or "").strip()
    return n if n else "do'stim"

def audience_sql(window):
    base = ("telegram_id != 0 AND is_banned=false AND is_active=true "
            "AND bot_blocked=false AND deleted_at IS NULL")
    days = {"3d": 3, "7d": 7, "14d": 14, "30d": 30}
    if window in days:
        base += (" AND (last_active_at IS NULL OR last_active_at < now() - interval '%d days')"
                 % days[window])
    return ("SELECT telegram_id, COALESCE(first_name,'') FROM users WHERE "
            + base + " ORDER BY id;")

def fetch_users(window):
    sql = audience_sql(window)
    out = subprocess.check_output(
        ["docker", "exec", "speakup-postgres", "psql", "-U", "speakup",
         "-d", "speakup_db", "-tA", "-F", "|", "-c", sql]).decode()
    users = []
    for line in out.splitlines():
        line = line.strip()
        if not line:
            continue
        parts = line.split("|", 1)
        tid = parts[0].strip()
        name = parts[1] if len(parts) > 1 else ""
        if tid:
            users.append((tid, name))
    return users

def send(chat_id, text):
    markup = json.dumps({"inline_keyboard": [[{"text": BTN, "web_app": {"url": MINIAPP}}]]})
    out = subprocess.run([
        "curl", "-s", "--max-time", "25",
        "--resolve", "api.telegram.org:443:%s" % TG_IP,
        API + "sendMessage",
        "--data-urlencode", "chat_id=%s" % chat_id,
        "--data-urlencode", "text=%s" % text,
        "--data-urlencode", "disable_web_page_preview=true",
        "--data-urlencode", "reply_markup=%s" % markup,
    ], capture_output=True, text=True, timeout=40).stdout
    return json.loads(out) if out.strip() else {"ok": False, "description": "empty response"}

def send_with_retry(chat_id, text):
    try:
        resp = send(chat_id, text)
    except Exception as e:
        return None, str(e)
    if resp.get("ok"):
        return resp["result"]["message_id"], None
    ra = resp.get("parameters", {}).get("retry_after", 0)
    if ra:
        time.sleep(ra + 1)
        try:
            resp = send(chat_id, text)
            if resp.get("ok"):
                return resp["result"]["message_id"], None
        except Exception as e:
            return None, str(e)
    return None, resp.get("description", "unknown")

def main():
    mode = sys.argv[1] if len(sys.argv) > 1 else "test"
    ts = datetime.datetime.now().strftime("%Y%m%d_%H%M%S")
    logpath = os.path.join(HERE, "backups", "reengage_%s.log" % ts)

    if mode == "test":
        print("TEST mode -> sending all %d variants to admin %s" % (len(VARIANTS), ADMIN_ID))
        for i, v in enumerate(VARIANTS):
            text = "[TEST %d/%d]\n\n" % (i + 1, len(VARIANTS)) + v.format(name="Jamshidbek")
            mid, err = send_with_retry(ADMIN_ID, text)
            print("  variant %d -> %s" % (i + 1, ("msg_id=%s" % mid) if mid else "FAIL: %s" % err))
            time.sleep(0.5)
        return

    window = sys.argv[2] if len(sys.argv) > 2 else "3d"
    users = fetch_users(window)
    print("LIVE mode | audience=%s | recipients=%d | log=%s" % (window, len(users), logpath))
    sent = failed = 0
    with open(logpath, "w") as log:
        for i, (tid, name) in enumerate(users):
            vidx = i % len(VARIANTS)
            text = VARIANTS[vidx].format(name=fallback_name(name))
            mid, err = send_with_retry(tid, text)
            if mid:
                log.write("%s %s %d\n" % (tid, mid, vidx))
                log.flush()
                sent += 1
            else:
                log.write("FAIL %s :: %s\n" % (tid, err))
                log.flush()
                failed += 1
            if (i + 1) % 25 == 0:
                time.sleep(1)
    print("DONE sent=%d failed=%d total=%d" % (sent, failed, len(users)))
    print("log=%s" % logpath)

if __name__ == "__main__":
    main()
