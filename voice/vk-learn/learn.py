"""vk-learn: the learning service of the voice dictation server (Phase 2).

Runs inside the voice docker-compose network only; the gateway forwards
/v1/vk/* to it after checking the API key and sets X-VK-User / X-VK-Device
from the key (clients cannot choose their identity). Stores per person:
corrections (what dictation wrote -> what the user changed it to), final
messages (opt-in in the app), and what is learned from them (vocabulary, fix
rules). Raw corrections and messages are kept RETENTION_DAYS (1 year).
Python standard library only; data in SQLite at /data/learn.db.

Endpoints (all JSON; paths relative to /v1/vk/):
  GET    me                 -> {"user","device"}
  POST   corrections        {"device","app","lang","stt_model","edits":[{"before","after","context"}],"ts"}
  POST   messages           {"app","category","text","lang"}        (alias: POST style)
  GET    profile?app=<pkg>  -> {"version","vocabulary","replacements","whisper_prompt",
                                "cleanup_style","style_examples"}
  GET    learned            -> {"vocabulary":[...],"replacements":[...]}
  DELETE learned/<id>       (id like "v12" or "r3")
  GET    export             -> everything stored for this user
  DELETE data               -> forget everything for this user
  GET    health             (no user needed)
"""
import json, os, re, sqlite3, threading, time
from datetime import datetime, timezone, timedelta
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse, parse_qs

DB = os.environ.get("VK_DB", "/data/learn.db")
RETENTION_DAYS = int(os.environ.get("VK_RETENTION_DAYS", "365"))
PROMOTE_AFTER = 2          # identical correction seen this often -> automatic fix rule
MAX_EDITS, MAX_LEN, MAX_MSG = 20, 200, 4000
VOCAB_CAP, PROMPT_CHARS = 100, 600
WORD = re.compile(r"[A-Za-z][A-Za-z0-9'’.-]*[A-Za-z0-9]|[A-Za-z]")
lock = threading.Lock()


def now():
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def db():
    c = sqlite3.connect(DB, timeout=10)
    c.row_factory = sqlite3.Row
    return c


def init():
    os.makedirs(os.path.dirname(DB), exist_ok=True)
    with db() as c:
        c.executescript("""
        PRAGMA journal_mode=WAL;
        CREATE TABLE IF NOT EXISTS corrections(
          id INTEGER PRIMARY KEY, user TEXT NOT NULL, device TEXT, app TEXT, lang TEXT,
          stt_model TEXT, before TEXT, after TEXT, context TEXT, ts TEXT, created TEXT NOT NULL);
        CREATE INDEX IF NOT EXISTS corr_user ON corrections(user, created);
        CREATE TABLE IF NOT EXISTS messages(
          id INTEGER PRIMARY KEY, user TEXT NOT NULL, device TEXT, app TEXT, category TEXT,
          lang TEXT, text TEXT NOT NULL, created TEXT NOT NULL);
        CREATE INDEX IF NOT EXISTS msg_user ON messages(user, category, created);
        CREATE TABLE IF NOT EXISTS replacements(
          id INTEGER PRIMARY KEY, user TEXT NOT NULL, src TEXT NOT NULL, dst TEXT NOT NULL,
          count INTEGER NOT NULL DEFAULT 0, last_seen TEXT, UNIQUE(user, src, dst));
        CREATE TABLE IF NOT EXISTS vocabulary(
          id INTEGER PRIMARY KEY, user TEXT NOT NULL, word TEXT NOT NULL,
          count INTEGER NOT NULL DEFAULT 0, last_seen TEXT, UNIQUE(user, word));
        CREATE TABLE IF NOT EXISTS meta(user TEXT PRIMARY KEY, version INTEGER NOT NULL DEFAULT 0,
          cleanup_style TEXT NOT NULL DEFAULT '');
        """)


def bump(c, user):
    c.execute("INSERT INTO meta(user, version) VALUES(?, 1) "
              "ON CONFLICT(user) DO UPDATE SET version = version + 1", (user,))


def clean(s, limit):
    if not isinstance(s, str):
        return ""
    return s.replace("\x00", "").strip()[:limit]


def learn_edit(c, user, before, after):
    """Turn one correction into learned items. Returns True if anything changed."""
    if not before or not after or before == after:
        return False                       # pure insertions/deletions (e.g. removed filler) teach nothing
    if re.fullmatch(r"[\d\s.,:/-]+", before + after):
        return False                       # number/time formatting, not vocabulary
    if len(before.split()) > 6 or len(after.split()) > 6:
        return False                       # too big to be a single correction
    t = now()
    c.execute("INSERT INTO replacements(user, src, dst, count, last_seen) VALUES(?,?,?,1,?) "
              "ON CONFLICT(user, src, dst) DO UPDATE SET count = count + 1, last_seen = excluded.last_seen",
              (user, before, after, t))
    before_words = {w.lower() for w in WORD.findall(before)}
    for w in WORD.findall(after):
        if len(w) >= 2 and w.lower() not in before_words:
            c.execute("INSERT INTO vocabulary(user, word, count, last_seen) VALUES(?,?,1,?) "
                      "ON CONFLICT(user, word) DO UPDATE SET count = count + 1, last_seen = excluded.last_seen",
                      (user, w, t))
    return True


def profile(c, user, app=None):
    meta = c.execute("SELECT version, cleanup_style FROM meta WHERE user=?", (user,)).fetchone()
    vocab = [r["word"] for r in c.execute(
        "SELECT word FROM vocabulary WHERE user=? ORDER BY count DESC, last_seen DESC LIMIT ?", (user, VOCAB_CAP))]
    reps = [{"from": r["src"], "to": r["dst"], "count": r["count"]} for r in c.execute(
        "SELECT src, dst, count FROM replacements WHERE user=? AND count >= ? ORDER BY count DESC",
        (user, PROMOTE_AFTER))]
    prompt = ""
    for w in vocab:                        # Whisper "prompt": known words, kept short
        nxt = (prompt + ", " + w) if prompt else w
        if len(nxt) > PROMPT_CHARS:
            break
        prompt = nxt
    q = "SELECT text FROM messages WHERE user=?"
    args = [user]
    if app:
        q += " AND app=?"; args.append(app)
    examples = [r["text"] for r in c.execute(q + " ORDER BY created DESC LIMIT 3", args)]
    if app and not examples:
        examples = [r["text"] for r in c.execute(
            "SELECT text FROM messages WHERE user=? ORDER BY created DESC LIMIT 3", (user,))]
    return {"version": meta["version"] if meta else 0, "vocabulary": vocab, "replacements": reps,
            "whisper_prompt": (prompt + ".") if prompt else "",
            "cleanup_style": meta["cleanup_style"] if meta else "", "style_examples": examples}


def purge():
    cutoff = (datetime.now(timezone.utc) - timedelta(days=RETENTION_DAYS)).isoformat(timespec="seconds")
    with lock, db() as c:
        a = c.execute("DELETE FROM corrections WHERE created < ?", (cutoff,)).rowcount
        b = c.execute("DELETE FROM messages WHERE created < ?", (cutoff,)).rowcount
    if a or b:
        print(f"retention: removed {a} corrections, {b} messages older than {RETENTION_DAYS} days", flush=True)


def purge_loop():
    while True:
        try:
            purge()
        except Exception as e:  # keep the service alive
            print("purge error:", e, flush=True)
        time.sleep(6 * 3600)


class H(BaseHTTPRequestHandler):
    server_version = "vk-learn"

    def reply(self, code, obj):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def ident(self):
        user = (self.headers.get("X-VK-User") or "").strip()
        return user, (self.headers.get("X-VK-Device") or "").strip()

    def body(self):
        n = int(self.headers.get("Content-Length") or 0)
        if n > 256 * 1024:
            raise ValueError("body too large")
        return json.loads(self.rfile.read(n) or b"{}")

    def route(self):
        u = urlparse(self.path)
        p = u.path
        for prefix in ("/v1/vk/", "/vk/", "/"):
            if p.startswith(prefix):
                p = p[len(prefix):]
                break
        return p.strip("/"), parse_qs(u.query)

    def do_GET(self):
        path, q = self.route()
        if path == "health":
            return self.reply(200, {"ok": True})
        user, device = self.ident()
        if not user:
            return self.reply(401, {"error": "no user"})
        with db() as c:
            if path == "me":
                return self.reply(200, {"user": user, "device": device})
            if path == "profile":
                return self.reply(200, profile(c, user, (q.get("app") or [None])[0]))
            if path == "learned":
                v = [{"id": f"v{r['id']}", "word": r["word"], "count": r["count"], "last_seen": r["last_seen"]}
                     for r in c.execute("SELECT * FROM vocabulary WHERE user=? ORDER BY count DESC", (user,))]
                r_ = [{"id": f"r{r['id']}", "from": r["src"], "to": r["dst"], "count": r["count"],
                       "active": r["count"] >= PROMOTE_AFTER, "last_seen": r["last_seen"]}
                      for r in c.execute("SELECT * FROM replacements WHERE user=? ORDER BY count DESC", (user,))]
                return self.reply(200, {"vocabulary": v, "replacements": r_})
            if path == "export":
                out = {}
                for t in ("corrections", "messages", "replacements", "vocabulary", "meta"):
                    out[t] = [dict(r) for r in c.execute(f"SELECT * FROM {t} WHERE user=?", (user,))]
                return self.reply(200, out)
        self.reply(404, {"error": "not found"})

    def do_POST(self):
        path, _ = self.route()
        user, device = self.ident()
        if not user:
            return self.reply(401, {"error": "no user"})
        try:
            b = self.body()
        except Exception:
            return self.reply(400, {"error": "invalid JSON"})
        if path == "corrections":
            edits = b.get("edits") if isinstance(b.get("edits"), list) else []
            if not edits:
                return self.reply(400, {"error": "edits must be a non-empty list"})
            learned, t = 0, now()
            with lock, db() as c:
                for e in edits[:MAX_EDITS]:
                    if not isinstance(e, dict):
                        continue
                    before, after = clean(e.get("before"), MAX_LEN), clean(e.get("after"), MAX_LEN)
                    c.execute("INSERT INTO corrections(user, device, app, lang, stt_model, before, after, context, ts, created)"
                              " VALUES(?,?,?,?,?,?,?,?,?,?)",
                              (user, clean(b.get("device"), 64) or device, clean(b.get("app"), 128),
                               clean(b.get("lang"), 8), clean(b.get("stt_model"), 128), before, after,
                               clean(e.get("context"), MAX_LEN), clean(b.get("ts"), 40), t))
                    learned += learn_edit(c, user, before, after)
                if learned:
                    bump(c, user)
            return self.reply(200, {"ok": True, "stored": min(len(edits), MAX_EDITS), "learned": learned})
        if path in ("messages", "style"):
            text = clean(b.get("text"), MAX_MSG)
            if not text:
                return self.reply(400, {"error": "text required"})
            cat = clean(b.get("category"), 16) or "other"
            with lock, db() as c:
                c.execute("INSERT INTO messages(user, device, app, category, lang, text, created) VALUES(?,?,?,?,?,?,?)",
                          (user, device, clean(b.get("app"), 128), cat, clean(b.get("lang"), 8), text, now()))
                bump(c, user)
            return self.reply(200, {"ok": True})
        self.reply(404, {"error": "not found"})

    def do_DELETE(self):
        path, _ = self.route()
        user, _d = self.ident()
        if not user:
            return self.reply(401, {"error": "no user"})
        with lock, db() as c:
            if path == "data":
                counts = {t: c.execute(f"DELETE FROM {t} WHERE user=?", (user,)).rowcount
                          for t in ("corrections", "messages", "replacements", "vocabulary", "meta")}
                return self.reply(200, {"ok": True, "deleted": counts})
            m = re.fullmatch(r"learned/([vr])(\d+)", path)
            if m:
                table = "vocabulary" if m.group(1) == "v" else "replacements"
                n = c.execute(f"DELETE FROM {table} WHERE id=? AND user=?", (int(m.group(2)), user)).rowcount
                if n:
                    bump(c, user)
                return self.reply(200 if n else 404, {"ok": bool(n)})
        self.reply(404, {"error": "not found"})

    def log_message(self, fmt, *args):     # no request bodies in logs; paths only
        print("%s %s" % (self.command, self.path.split("?")[0]), flush=True)


if __name__ == "__main__":
    init()
    threading.Thread(target=purge_loop, daemon=True).start()
    print(f"vk-learn on :8082, db {DB}, retention {RETENTION_DAYS} days", flush=True)
    ThreadingHTTPServer(("0.0.0.0", 8082), H).serve_forever()
