#!/usr/bin/env python3
# Roll back a re-engagement wave: deletes every message recorded in a
# reengage_*.log. Telegram only lets a bot delete its own messages
# within 48h, so older waves may partially fail.
#
# Usage: python3 reengage_undo.py ~/speakup/backups/reengage_<ts>.log
import json, os, sys, time, subprocess

HERE = os.path.expanduser("~/speakup")
TG_IP = "149.154.167.220"

def env(key):
    with open(os.path.join(HERE, ".env")) as f:
        for line in f:
            if line.startswith(key + "="):
                return line.split("=", 1)[1].strip()
    return ""

API = "https://api.telegram.org/bot%s/" % env("BOT_TOKEN")

def delete(chat_id, msg_id):
    try:
        out = subprocess.run([
            "curl", "-s", "--max-time", "15",
            "--resolve", "api.telegram.org:443:%s" % TG_IP,
            API + "deleteMessage",
            "--data-urlencode", "chat_id=%s" % chat_id,
            "--data-urlencode", "message_id=%s" % msg_id,
        ], capture_output=True, text=True, timeout=25).stdout
        return json.loads(out).get("ok", False) if out.strip() else False
    except Exception:
        return False

def main():
    path = sys.argv[1]
    deleted = failed = 0
    with open(path) as f:
        lines = [l.strip() for l in f if l.strip() and not l.startswith("FAIL")]
    print("Deleting %d messages from %s" % (len(lines), path))
    for i, line in enumerate(lines):
        parts = line.split()
        if len(parts) < 2:
            continue
        tid, mid = parts[0], parts[1]
        if delete(tid, mid):
            deleted += 1
        else:
            failed += 1
        if (i + 1) % 25 == 0:
            time.sleep(1)
    print("DONE deleted=%d failed=%d" % (deleted, failed))

if __name__ == "__main__":
    main()
