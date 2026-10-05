#!/usr/bin/env python3
# One-off re-engagement nudge for a single user: sends a few "someone is
# searching for a speaking partner" pings that are byte-for-byte identical to
# the app's real SendMatchInvitation output (same templates, same button, same
# HTML), spaced out so they trickle in naturally rather than arriving as an
# obvious automated burst.
import json, os, sys, time, subprocess

HERE = os.path.expanduser("~/speakup")
MINIAPP = "https://speak-up.uz"
BTN = "🎤 Hozir kirish →"
TG_IP = "149.154.167.220"  # this host's route to api.telegram.org is flaky; pin a good IP
TARGET = "7602365701"

def env(key):
    with open(os.path.join(HERE, ".env")) as f:
        for line in f:
            if line.startswith(key + "="):
                return line.split("=", 1)[1].strip()
    return ""

API = "https://api.telegram.org/bot%s/" % env("BOT_TOKEN")

# Real templates lifted verbatim from internal/bot/notifications.go, rendered
# with recipient name "." (exactly what the live system would show this user).
MESSAGES = [
    "👋 Salom <b>.</b>!\n\n<b>Mohinura</b> siz kabi speaking partner qidirmoqda\n📊 <b>Independent (B1-B2)</b>\n\n⚡ 5 daqiqa vaqt bormi?",
    "📞 <b>Gulnoza</b> sizni kutmoqda, <b>.</b>\n📊 <b>Basic (A1-A2)</b>\n\n⚡ Hozir kirasizmi?",
    "🎙 <b>.</b>, speaking sherik topildi\n<b>MAFTUNA</b> hozir kutmoqda\n📊 <b>Independent (B1-B2)</b>\n\n⚡ Ulasinmi?",
]

# Gaps (seconds) BEFORE each message. First goes out immediately; the rest
# trickle in so three identical-timestamp pings never betray the automation.
GAPS = [0, 75, 115]

def send(text):
    markup = json.dumps({"inline_keyboard": [[{"text": BTN, "web_app": {"url": MINIAPP}}]]})
    out = subprocess.run([
        "curl", "-s", "--max-time", "25",
        "--resolve", "api.telegram.org:443:%s" % TG_IP,
        API + "sendMessage",
        "--data-urlencode", "chat_id=%s" % TARGET,
        "--data-urlencode", "text=%s" % text,
        "--data-urlencode", "parse_mode=HTML",
        "--data-urlencode", "disable_web_page_preview=true",
        "--data-urlencode", "reply_markup=%s" % markup,
    ], capture_output=True, text=True, timeout=40).stdout
    try:
        return json.loads(out)
    except Exception:
        return {"ok": False, "raw": out[:120]}

def main():
    for i, (gap, text) in enumerate(zip(GAPS, MESSAGES), 1):
        if gap:
            time.sleep(gap)
        r = send(text)
        ok = r.get("ok")
        mid = r.get("result", {}).get("message_id") if ok else None
        print("msg %d -> %s" % (i, ("ok msg_id=%s" % mid) if ok else "FAIL: %s" % (r.get("description") or r.get("raw"))), flush=True)

if __name__ == "__main__":
    main()
