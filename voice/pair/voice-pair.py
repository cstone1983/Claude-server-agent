#!/usr/bin/env python3
"""voice-pair: set up the Voice Keyboard app from the home Wi-Fi, approved by the owner in Telegram.

Contract: voice-keyboard repo, docs/server-pairing.md. Runs as root on the voice VM (systemd
voice-pair.service), listening only on the Docker bridge (172.18.0.1:8083) so that only the voice
gateway reaches it. The gateway (voice.conf) lets /v1/vk/pair* through WITHOUT an API key but only
from the home LAN (10.10.1.0/24, real client address; the Cloudflare tunnel arrives as 172.18.0.1
and Traefik as 10.10.11.102, both refused), with rate limits.

  GET  /v1/vk/pair/info          -> {"name","base_url","people":[{"user","name"}]}
  POST /v1/vk/pair               {"person":<id>} | {"new_name":<name>}, "device":<name>
                                 -> 202 {"request_id","code","expires_in":600}; Telegram to the owner
  GET  /v1/vk/pair/<request_id>  -> {"status":"pending|approved|denied|expired"}; approved adds
                                 "setup" (same object as the setup QR) ONCE, then the request is gone.

Approve / Deny only from the owner's chat (callback chat id AND sender id checked). Approving a new
person adds them first (id from the name, made unique). Keys are made with voice-user's own code,
so they show in `voice-user list` with the device name and can be revoked there.

Config (root-only): /root/.config/voice/pair.conf   chat_id=<owner chat>  name=<server display name>
                    /root/.config/voice/pair-bot.token   (Telegram bot token, one line)
Nothing logs keys, codes or tokens.
"""
import importlib.machinery, importlib.util, json, os, re, secrets, sys, threading, time, urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LISTEN = (os.environ.get("PAIR_LISTEN", "172.18.0.1"), int(os.environ.get("PAIR_PORT", "8083")))
CONF = "/root/.config/voice/pair.conf"
TOKEN_FILE = "/root/.config/voice/pair-bot.token"
TTL = 600                 # an unanswered request expires after 10 minutes
KEEP = 24 * 3600          # denied/expired requests still answer with their status for a day
MAX_OPEN = 3              # at most this many requests waiting for approval at once

# voice-user's own functions (people, keys, gateway key map)
_loader = importlib.machinery.SourceFileLoader("voice_user", "/usr/local/sbin/voice-user")
_spec = importlib.util.spec_from_loader("voice_user", _loader)
vu = importlib.util.module_from_spec(_spec)
_loader.exec_module(vu)


def conf():
    c = {"chat_id": "", "name": "Voice server"}
    try:
        for line in open(CONF):
            if "=" in line and not line.lstrip().startswith("#"):
                k, v = line.split("=", 1)
                c[k.strip()] = v.strip()
    except OSError:
        pass
    return c


def token():
    try:
        return open(TOKEN_FILE).read().strip()
    except OSError:
        return ""


def tg(method, **params):
    t = token()
    if not t:
        raise RuntimeError("no Telegram bot token")
    req = urllib.request.Request(f"https://api.telegram.org/bot{t}/{method}",
                                 data=json.dumps(params).encode(), headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=70) as r:
        d = json.load(r)
    if not d.get("ok"):
        raise RuntimeError(f"telegram {method}: {d.get('description')}")
    return d.get("result")


lock = threading.Lock()
requests = {}             # request_id -> dict
by_aid = {}               # short approval id (in the Telegram button) -> request_id


def clean_device(s):
    s = re.sub(r"[^A-Za-z0-9 _.-]", " ", str(s or "")).strip()
    s = re.sub(r"\s+", " ", s)[:32].strip()
    return s if vu.NAME_RE.match(s or "") else "phone"


def clean_name(s):
    s = re.sub(r"[^A-Za-z0-9 _.-]", "", str(s or "")).strip()
    s = re.sub(r"\s+", " ", s)[:32].strip()
    return s if vu.NAME_RE.match(s or "") else ""


def people():
    users, _ = vu.load()
    return [{"user": u["user"], "name": u["name"]} for u in users if u["user"] != "*"]


def expire():
    now = time.time()
    for rid, r in list(requests.items()):
        if r["status"] == "pending" and now - r["created"] > TTL:
            r["status"] = "expired"
            edit_message(r, f"⌛ Expired (not answered in 10 min)")
        if r["status"] in ("denied", "expired") and now - r["created"] > KEEP:
            requests.pop(rid, None); by_aid.pop(r["aid"], None)


def who(r):
    return f"{r['person_name']} ({r['person']})" if r["person"] else f"new person {r['new_name']}"


def edit_message(r, line):
    if not r.get("msg_id"):
        return
    try:
        tg("editMessageText", chat_id=conf()["chat_id"], message_id=r["msg_id"],
           text=r["text"] + "\n\n" + line)
    except Exception as e:
        print(f"telegram edit failed: {e}", flush=True)


def new_user_id(name, users):
    base = re.sub(r"[^a-z0-9]+", "", name.lower()) or "person"
    base = base[:24]
    uid, n = base, 2
    while any(u["user"] == uid for u in users) or uid == "*":
        uid, n = f"{base}{n}", n + 1
    return uid


def approve(r):
    """Create the person (if new) and a personal key for the device; store the setup object."""
    users, keys = vu.load()
    if r["person"]:
        uid = r["person"]
        if not any(u["user"] == uid for u in users):
            raise RuntimeError(f"person {uid} no longer exists")
        name = next(u["name"] for u in users if u["user"] == uid)
    else:
        uid, name = new_user_id(r["new_name"], users), r["new_name"]
        users.append({"user": uid, "name": name})
        vu.write_tsv(vu.USERS, vu.UCOLS, users)
        vu.publish_users(users)
    nums = [int(k["id"][1:]) for k in keys if k["id"][1:].isdigit()]
    row = {"id": f"k{max(nums, default=0) + 1}", "user": uid, "device": r["device"],
           "key": secrets.token_hex(32), "created": time.strftime("%Y-%m-%d"), "revoked": "0"}
    keys.append(row)
    vu.write_tsv(vu.KEYS, vu.KCOLS, keys)
    vu.sync()                                   # gateway key map + nginx reload
    r["setup"] = {"v": 1, "base_url": vu.BASE_URL, "api_key": row["key"], "stt_model": vu.STT,
                  "chat_model": vu.CHAT, "user": name, "user_id": uid, "device": r["device"]}
    r["key_id"], r["user_id"] = row["id"], uid


def telegram_loop():
    offset = 0
    while True:
        if not token():
            time.sleep(30); continue
        try:
            ups = tg("getUpdates", offset=offset, timeout=50, allowed_updates=["callback_query"])
        except Exception as e:
            print(f"telegram poll: {e}", flush=True); time.sleep(10); continue
        for u in ups or []:
            offset = u["update_id"] + 1
            cq = u.get("callback_query")
            if not cq:
                continue
            owner = str(conf()["chat_id"])
            chat = str(((cq.get("message") or {}).get("chat") or {}).get("id", ""))
            sender = str((cq.get("from") or {}).get("id", ""))
            data = cq.get("data") or ""
            m = re.fullmatch(r"vkp:([ad]):([A-Za-z0-9_-]{8,32})", data)
            answer = "Not allowed"
            if owner and chat == owner and sender == owner and m:
                with lock:
                    expire()
                    rid = by_aid.get(m.group(2)); r = requests.get(rid) if rid else None
                    if not r:
                        answer = "This request is gone (expired)"
                    elif r["status"] != "pending":
                        answer = f"Already {r['status']}"
                    elif m.group(1) == "d":
                        r["status"] = "denied"; answer = "Denied"
                        edit_message(r, "❌ Denied")
                    else:
                        try:
                            approve(r)
                            r["status"] = "approved"; answer = "Approved"
                            edit_message(r, f"✅ Approved: key {r['key_id']} for {r['user_id']} / {r['device']}"
                                            f"\n(revoke: sudo voice-user revoke {r['key_id']})")
                        except Exception as e:
                            answer = "Approval failed"
                            print(f"approve failed: {type(e).__name__}: {e}", flush=True)
                            edit_message(r, "⚠️ Approval failed on the server - see journalctl -u voice-pair")
            elif m:
                print(f"refused a button press from chat {chat} / sender {sender}", flush=True)
            try:
                tg("answerCallbackQuery", callback_query_id=cq["id"], text=answer)
            except Exception:
                pass


class H(BaseHTTPRequestHandler):
    def reply(self, code, obj):
        b = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(b)

    def lan_ok(self):
        # The gateway already refused non-LAN clients; this is a second check on what it passed on.
        ip = self.headers.get("X-Pair-Client", "")
        return ip.startswith("10.10.1.") and not self.headers.get("X-Pair-Via-Tunnel")

    def do_GET(self):
        if not self.lan_ok():
            return self.reply(403, {"error": "home network only"})
        path = self.path.split("?")[0].rstrip("/")
        if path == "/v1/vk/pair/info":
            return self.reply(200, {"name": conf()["name"], "base_url": vu.BASE_URL, "people": people()})
        m = re.fullmatch(r"/v1/vk/pair/([A-Za-z0-9_-]{20,64})", path)
        if not m:
            return self.reply(404, {"error": "not found"})
        with lock:
            expire()
            r = requests.get(m.group(1))
            if not r:
                return self.reply(404, {"error": "unknown or finished request"})
            if r["status"] == "approved":
                out = {"status": "approved", "setup": r["setup"]}
                requests.pop(m.group(1), None); by_aid.pop(r["aid"], None)     # handed out once
                print(f"pair: setup handed out for {r['user_id']} / {r['device']} ({r['key_id']})", flush=True)
                return self.reply(200, out)
            return self.reply(200, {"status": r["status"]})

    def do_POST(self):
        if not self.lan_ok():
            return self.reply(403, {"error": "home network only"})
        if self.path.split("?")[0].rstrip("/") != "/v1/vk/pair":
            return self.reply(404, {"error": "not found"})
        try:
            n = int(self.headers.get("Content-Length") or 0)
            b = json.loads(self.rfile.read(min(n, 4096)) or b"{}") if n <= 4096 else None
        except Exception:
            b = None
        if not isinstance(b, dict):
            return self.reply(400, {"error": "invalid JSON"})
        device = clean_device(b.get("device"))
        person, new_name = str(b.get("person") or "").strip().lower(), clean_name(b.get("new_name"))
        ppl = {p["user"]: p["name"] for p in people()}
        if person:
            if person not in ppl:
                return self.reply(400, {"error": "unknown person"})
            new_name = ""
        elif not new_name:
            return self.reply(400, {"error": "person or new_name required"})
        c = conf()
        if not token() or not c["chat_id"]:
            return self.reply(503, {"error": "approval by the owner is not set up on the server yet"})
        with lock:
            expire()
            if sum(1 for r in requests.values() if r["status"] == "pending") >= MAX_OPEN:
                return self.reply(429, {"error": "too many requests waiting for approval, try again later"})
            rid, aid = secrets.token_urlsafe(32), secrets.token_urlsafe(12)
            code = f"{secrets.randbelow(10000):04d}"
            r = {"status": "pending", "created": time.time(), "aid": aid, "code": code, "device": device,
                 "person": person, "person_name": ppl.get(person, ""), "new_name": new_name,
                 "client": self.headers.get("X-Pair-Client", "?"), "msg_id": None}
            r["text"] = (f"📱 Voice Keyboard setup request\n"
                         f"Person: {who(r)}\nDevice: {device}\nCode: {code}\nFrom: {r['client']} (home Wi-Fi)\n"
                         f"Check the same code shows on the phone. Expires in 10 min.")
            try:
                msg = tg("sendMessage", chat_id=c["chat_id"], text=r["text"], reply_markup={"inline_keyboard": [[
                    {"text": "✅ Approve", "callback_data": f"vkp:a:{aid}"},
                    {"text": "❌ Deny", "callback_data": f"vkp:d:{aid}"}]]})
                r["msg_id"] = msg.get("message_id")
            except Exception as e:
                print(f"pair: telegram send failed: {e}", flush=True)
                return self.reply(503, {"error": "could not reach the owner, try again later"})
            requests[rid] = r; by_aid[aid] = rid
        print(f"pair: request from {r['client']} for {who(r)} / {device}", flush=True)
        return self.reply(202, {"request_id": rid, "code": code, "expires_in": TTL})

    def log_message(self, *a):
        pass


if __name__ == "__main__":
    if os.geteuid() != 0:
        sys.exit("voice-pair: run as root")
    threading.Thread(target=telegram_loop, daemon=True).start()
    print(f"voice-pair on {LISTEN[0]}:{LISTEN[1]}", flush=True)
    ThreadingHTTPServer(LISTEN, H).serve_forever()
