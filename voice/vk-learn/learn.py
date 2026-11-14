"""vk-learn: the learning service of the voice dictation server.

Runs inside the voice docker-compose network only; the gateway forwards
/v1/vk/* to it after checking the API key and sets X-VK-User / X-VK-Device
from the key (clients cannot choose their identity). Everything is stored per
person and kept RETENTION_DAYS (1 year); learned items stay until deleted.
Python standard library only; data in SQLite at /data/learn.db, audio clips
in /data/audio/<user>/.

What it learns from:
  1. corrections  - what dictation inserted -> what the user changed it to
                    (vocabulary + fix rules; everyday-word fixes never become rules)
  2. messages     - final sent texts (opt-in) -> nightly writing-style summary per
                    category (chat/email/other) made by the local model
  3. dictations   - per dictation: what Whisper heard -> what cleanup made -> what
                    was sent. Learns words cleanup adds that the user removes
                    ("cleanup_avoid") and low-confidence words the user keeps
                    (vocabulary hints)
  4. audio        - opt-in clips paired with the final text, training data for a
                    later voice fine-tune (see /app/README-training.md)
  + stats         - daily accuracy (words dictated vs. words corrected)

Endpoints (JSON; paths relative to /v1/vk/):
  GET    users              -> {"users":[{"user","name"}]}  (names for the app's picker)
  GET    me                 -> {"user","name","device","household"}
  A "household" key (mapped to user "*") may act as any listed user: the app
  sends the selected one as header X-VK-As. A personal key is always its own user.
  POST   corrections        {"device","app","lang","stt_model","edits":[{"before","after","context"}],"ts"}
  POST   messages           {"app","category","text","lang"}        (alias: POST style)
  POST   dictations         {"id","app","category","lang","stt_model","raw","cleaned","final",
                             "audio_ms","low_confidence":["word",...],"timings":{...}}
  POST   audio?dictation=<id>   body = the audio (Content-Type audio/wav, audio/ogg, ...)
  GET    profile?app=<pkg>[&category=chat|email|other]
                            -> {"version","vocabulary","replacements","whisper_prompt",
                                "cleanup_style","cleanup_avoid","style_examples"}
  GET    stats              -> {"days":[...], "totals":{...}, "audio":{...}}
  GET    learned            -> {"vocabulary":[...],"replacements":[...],"avoid":[...]}
  DELETE learned/<id>       (id like "v12", "r3" or "a5")
  POST   style/rebuild      rebuild the writing-style summaries now (normally nightly)
  DELETE audio              delete all stored audio clips of this user
  GET    export             -> everything stored for this user (audio as file list)
  DELETE data               -> forget everything for this user (audio included)
  GET    health             (no user needed)
"""
import difflib, json, os, re, shutil, sqlite3, threading, time, urllib.request
from datetime import datetime, timezone, timedelta
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse, parse_qs

DB = os.environ.get("VK_DB", "/data/learn.db")
AUDIO_DIR = os.environ.get("VK_AUDIO", os.path.join(os.path.dirname(DB), "audio"))
USERS_FILE = os.environ.get("VK_USERS", "/config/users.tsv")   # user \t display name (no keys)
RETENTION_DAYS = int(os.environ.get("VK_RETENTION_DAYS", "365"))
OLLAMA = os.environ.get("VK_OLLAMA", "http://ollama:11434")
STYLE_MODEL = os.environ.get("VK_STYLE_MODEL", "qwen2.5:7b")
STYLE_HOUR_UTC = int(os.environ.get("VK_STYLE_HOUR_UTC", "8"))   # 08:00 UTC = 03:00 Eastern
PROMOTE_AFTER = 2          # identical correction / removal seen this often -> active
MAX_EDITS, MAX_LEN, MAX_MSG, MAX_AUDIO = 20, 200, 4000, 20 * 1024 * 1024
VOCAB_CAP, PROMPT_CHARS = 100, 600
WORD = re.compile(r"[A-Za-z][A-Za-z0-9'’.-]*[A-Za-z0-9]|[A-Za-z]")
AUDIO_TYPES = {"audio/wav": "wav", "audio/x-wav": "wav", "audio/wave": "wav", "audio/ogg": "ogg",
               "audio/opus": "opus", "audio/mp4": "m4a", "audio/m4a": "m4a", "audio/mpeg": "mp3",
               "audio/webm": "webm", "audio/flac": "flac", "application/octet-stream": "bin"}
lock = threading.Lock()


def users():
    out = []
    try:
        for line in open(USERS_FILE):
            parts = line.rstrip("\n").split("\t")
            if parts and parts[0] and not parts[0].startswith("#"):
                out.append({"user": parts[0], "name": parts[1] if len(parts) > 1 and parts[1] else parts[0]})
    except FileNotFoundError:
        pass
    return out


def now():
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def db():
    c = sqlite3.connect(DB, timeout=10)
    c.row_factory = sqlite3.Row
    return c


def init():
    os.makedirs(os.path.dirname(DB), exist_ok=True)
    os.makedirs(AUDIO_DIR, exist_ok=True)
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
        CREATE TABLE IF NOT EXISTS dictations(
          id INTEGER PRIMARY KEY, user TEXT NOT NULL, client_id TEXT, device TEXT, app TEXT,
          category TEXT, lang TEXT, stt_model TEXT, raw TEXT, cleaned TEXT, final TEXT,
          audio_ms INTEGER, low_conf TEXT, timings TEXT, created TEXT NOT NULL);
        CREATE INDEX IF NOT EXISTS dict_user ON dictations(user, created);
        CREATE INDEX IF NOT EXISTS dict_client ON dictations(user, client_id);
        CREATE TABLE IF NOT EXISTS avoid(
          id INTEGER PRIMARY KEY, user TEXT NOT NULL, word TEXT NOT NULL,
          count INTEGER NOT NULL DEFAULT 0, last_seen TEXT, UNIQUE(user, word));
        CREATE TABLE IF NOT EXISTS styles(
          user TEXT NOT NULL, category TEXT NOT NULL, style TEXT NOT NULL,
          n_messages INTEGER, updated TEXT, PRIMARY KEY(user, category));
        CREATE TABLE IF NOT EXISTS audio(
          id INTEGER PRIMARY KEY, user TEXT NOT NULL, dictation_id INTEGER, path TEXT NOT NULL,
          text TEXT, bytes INTEGER, audio_ms INTEGER, created TEXT NOT NULL);
        CREATE INDEX IF NOT EXISTS audio_user ON audio(user, created);
        """)


def bump(c, user):
    c.execute("INSERT INTO meta(user, version) VALUES(?, 1) "
              "ON CONFLICT(user) DO UPDATE SET version = version + 1", (user,))


def clean(s, limit):
    if not isinstance(s, str):
        return ""
    return s.replace("\x00", "").strip()[:limit]


EDGE_PUNCT = " \t\n,.;:!?\"'()[]{}…“”‘’"

# Everyday English words: fixes between these (this/the, there/their, to/too)
# depend on context, so they are stored but never become blind rules or
# vocabulary hints. Names and unusual words still do.
COMMON = set("""
a about above after again against all also am an and any are as at be because been before being below
between both but by can could did do does doing down during each few for from further had has have
having he her here hers herself him himself his how i if in into is it its itself just me more most my
myself no nor not now of off on once only or other our ours ourselves out over own same she should so
some such than that the their theirs them themselves then there these they this those through to too
under until up very was we were what when where which while who whom why will with would you your
yours yourself yourselves yes ok okay oh hi hey well like get got go going gone come came make made
know think see look want need one two three four five six seven eight nine ten first last new old good
great right left back still even way thing things time today tomorrow yesterday tonight let lets let's
im i'm it's its don't dont can't cant won't wont didn't didnt isn't isnt that's thats there's theres
you're youre they're theyre we're were i'll ill you'll youll we'll i've ive you've we've i'd id
""".split())


def all_common(text):
    words = [w.lower() for w in WORD.findall(text)]
    return bool(words) and all(w in COMMON for w in words)


def strip_shared_punct(before, after):
    """'Jesse,' -> 'Jessy,' becomes 'Jesse' -> 'Jessy': punctuation around the
    corrected word is not part of the correction (rules then match anywhere)."""
    return before.strip(EDGE_PUNCT), after.strip(EDGE_PUNCT)


def add_vocab(c, user, word, t):
    if len(word) >= 2 and word.lower() not in COMMON:
        c.execute("INSERT INTO vocabulary(user, word, count, last_seen) VALUES(?,?,1,?) "
                  "ON CONFLICT(user, word) DO UPDATE SET count = count + 1, last_seen = excluded.last_seen",
                  (user, word, t))
        return True
    return False


def learn_edit(c, user, before, after):
    """Turn one correction into learned items. Returns True if anything changed."""
    before, after = strip_shared_punct(before, after)
    if not before or not after or before == after:
        return False                       # pure insertions/deletions (e.g. removed filler) teach nothing
    if re.fullmatch(r"[\d\s.,:/-]+", before + after):
        return False                       # number/time formatting, not vocabulary
    if len(before.split()) > 6 or len(after.split()) > 6:
        return False                       # too big to be a single correction
    if all_common(before) and all_common(after):
        return False                       # grammar-style fix of everyday words: needs context, no blind rule
    t = now()
    c.execute("INSERT INTO replacements(user, src, dst, count, last_seen) VALUES(?,?,?,1,?) "
              "ON CONFLICT(user, src, dst) DO UPDATE SET count = count + 1, last_seen = excluded.last_seen",
              (user, before, after, t))
    before_words = {w.lower() for w in WORD.findall(before)}
    for w in WORD.findall(after):
        if w.lower() not in before_words:
            add_vocab(c, user, w, t)
    return True


def words(text):
    return [w.lower() for w in WORD.findall(text or "")]


def learn_dictation(c, user, raw, cleaned, final, low_conf):
    """Speaking habits from one dictation. Returns the number of learned items.
    - cleanup_avoid: words cleanup ADDED (in cleaned, not in raw) that the user
      REMOVED again (not in final) - e.g. an added "please".
    - vocabulary: low-confidence words Whisper heard that the user KEPT in final."""
    t, n = now(), 0
    if raw and cleaned and final:
        raw_w, final_w = set(words(raw)), set(words(final))
        for w in set(words(cleaned)):
            if w not in raw_w and w not in final_w and len(w) >= 2:
                c.execute("INSERT INTO avoid(user, word, count, last_seen) VALUES(?,?,1,?) "
                          "ON CONFLICT(user, word) DO UPDATE SET count = count + 1, last_seen = excluded.last_seen",
                          (user, w, t))
                n += 1
    if final and isinstance(low_conf, list):
        # Only name-like words: written with a capital letter somewhere other than
        # the start of a sentence (plain words like "grab" are not hints).
        names = {}
        for sentence in re.split(r"[.!?\n]+", final):
            for i, w in enumerate(WORD.findall(sentence)):
                if i > 0 and any(ch.isupper() for ch in w):
                    names[w.lower()] = w
        for w in low_conf[:30]:
            if isinstance(w, str):
                kept = names.get(w.strip(EDGE_PUNCT).lower())
                if kept and add_vocab(c, user, kept, t):
                    n += 1
    return n


def corrected_words(before, after):
    """How many words of the inserted text the user had to change."""
    b, a = words(before), words(after)
    sm = difflib.SequenceMatcher(a=b, b=a)
    return sum(i2 - i1 for op, i1, i2, j1, j2 in sm.get_opcodes() if op in ("replace", "delete"))


def category_of(c, user, app):
    if not app:
        return None
    r = c.execute("SELECT category FROM messages WHERE user=? AND app=? ORDER BY created DESC LIMIT 1",
                  (user, app)).fetchone() or c.execute(
        "SELECT category FROM dictations WHERE user=? AND app=? ORDER BY created DESC LIMIT 1",
        (user, app)).fetchone()
    return r["category"] if r else None


def profile(c, user, app=None, category=None):
    meta = c.execute("SELECT version, cleanup_style FROM meta WHERE user=?", (user,)).fetchone()
    vocab = [r["word"] for r in c.execute(
        "SELECT word FROM vocabulary WHERE user=? ORDER BY count DESC, last_seen DESC LIMIT ?", (user, VOCAB_CAP))]
    reps = [{"from": r["src"], "to": r["dst"], "count": r["count"]} for r in c.execute(
        "SELECT src, dst, count FROM replacements WHERE user=? AND count >= ? ORDER BY count DESC",
        (user, PROMOTE_AFTER))]
    avoid = [r["word"] for r in c.execute(
        "SELECT word FROM avoid WHERE user=? AND count >= ? ORDER BY count DESC LIMIT 30", (user, PROMOTE_AFTER))]
    prompt = ""
    for w in vocab:                        # Whisper "prompt": known words, kept short
        nxt = (prompt + ", " + w) if prompt else w
        if len(nxt) > PROMPT_CHARS:
            break
        prompt = nxt
    category = category or category_of(c, user, app)
    style = None
    if category:
        style = c.execute("SELECT style FROM styles WHERE user=? AND category=?", (user, category)).fetchone()
    style = style or c.execute("SELECT style FROM styles WHERE user=? AND category='all'", (user,)).fetchone()
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
            "cleanup_style": style["style"] if style else (meta["cleanup_style"] if meta else ""),
            "cleanup_avoid": avoid, "category": category or "", "style_examples": examples}


def stats(c, user, days=30):
    since = (datetime.now(timezone.utc) - timedelta(days=days)).isoformat(timespec="seconds")
    per = {}
    for r in c.execute("SELECT substr(created,1,10) d, count(*) n, sum(audio_ms) ms, "
                       "group_concat(cleaned, char(1)) txt FROM dictations WHERE user=? AND created>=? GROUP BY d",
                       (user, since)):
        per[r["d"]] = {"day": r["d"], "dictations": r["n"], "audio_s": round((r["ms"] or 0) / 1000),
                       "words": sum(len(words(t)) for t in (r["txt"] or "").split("\x01")), "corrected_words": 0,
                       "corrections": 0}
    for r in c.execute("SELECT substr(created,1,10) d, before, after FROM corrections WHERE user=? AND created>=?",
                       (user, since)):
        d = per.setdefault(r["d"], {"day": r["d"], "dictations": 0, "audio_s": 0, "words": 0,
                                    "corrected_words": 0, "corrections": 0})
        d["corrections"] += 1
        d["corrected_words"] += max(corrected_words(r["before"], r["after"]), 1 if r["before"] else 0)
    out = []
    for d in sorted(per):
        x = per[d]
        x["accuracy_pct"] = round(100 * (1 - x["corrected_words"] / x["words"]), 1) if x["words"] else None
        out.append(x)
    tot_w = sum(x["words"] for x in out)
    tot_c = sum(x["corrected_words"] for x in out)
    a = c.execute("SELECT count(*) n, coalesce(sum(audio_ms),0) ms, coalesce(sum(bytes),0) b FROM audio WHERE user=?",
                  (user,)).fetchone()
    return {"days": out,
            "totals": {"dictations": sum(x["dictations"] for x in out), "words": tot_w, "corrected_words": tot_c,
                       "accuracy_pct": round(100 * (1 - tot_c / tot_w), 1) if tot_w else None},
            "audio": {"clips": a["n"], "minutes": round(a["ms"] / 60000, 1), "megabytes": round(a["b"] / 1e6, 1),
                      "fine_tune_ready": a["ms"] >= 2 * 3600 * 1000}}


# ---------- writing style (nightly) ----------
STYLE_PROMPT = """You are given recent messages written by one person (dictated, then sent).
Describe HOW this person writes - never WHAT they write about - as at most 6 short bullet points
that a text-cleanup assistant can follow when tidying their dictation:
tone and formality, sentence length, how they open and close messages, emoji use, favourite
filler-free phrasings or expressions.
Rules: never mention topics, projects, technologies, products, apps, people, places or any
specific words from the messages. Do not describe their interests. Do not give punctuation or
grammar advice that would make the text less correct. Reply with the bullet points only."""


def ask_ollama(prompt, text, timeout=600):
    req = urllib.request.Request(f"{OLLAMA}/api/chat", json.dumps({
        "model": STYLE_MODEL, "stream": False, "keep_alive": "2m",
        "options": {"temperature": 0.2, "num_predict": 300},
        "messages": [{"role": "system", "content": prompt}, {"role": "user", "content": text}]}).encode(),
        {"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return json.load(r)["message"]["content"].strip()


def rebuild_styles(only_user=None):
    """Summarize each user's recent messages per category (and 'all')."""
    done = []
    with db() as c:
        rows = c.execute("SELECT DISTINCT user FROM messages" + (" WHERE user=?" if only_user else ""),
                         (only_user,) if only_user else ()).fetchall()
        targets = []
        for r in rows:
            u = r["user"]
            cats = [x["category"] for x in c.execute(
                "SELECT category, count(*) n FROM messages WHERE user=? GROUP BY category HAVING n >= 5", (u,))]
            for cat in cats + ["all"]:
                q = "SELECT text FROM messages WHERE user=?" + ("" if cat == "all" else " AND category=?")
                args = (u,) if cat == "all" else (u, cat)
                msgs = [x["text"] for x in c.execute(q + " ORDER BY created DESC LIMIT 40", args)]
                if len(msgs) >= 5:
                    targets.append((u, cat, msgs))
    for u, cat, msgs in targets:
        try:
            style = ask_ollama(STYLE_PROMPT, "\n---\n".join(m[:500] for m in msgs))
            style = "\n".join([l for l in style.splitlines() if l.strip()][:6])   # hard cap: 6 points
        except Exception as e:
            print(f"style {u}/{cat} failed: {e}", flush=True)
            continue
        with lock, db() as c:
            c.execute("INSERT INTO styles(user, category, style, n_messages, updated) VALUES(?,?,?,?,?) "
                      "ON CONFLICT(user, category) DO UPDATE SET style=excluded.style, "
                      "n_messages=excluded.n_messages, updated=excluded.updated", (u, cat, style[:1200], len(msgs), now()))
            bump(c, u)
        done.append(f"{u}/{cat}")
    print(f"styles rebuilt: {done}", flush=True)
    return done


def nightly_loop():
    last = None
    while True:
        n = datetime.now(timezone.utc)
        if n.hour == STYLE_HOUR_UTC and last != n.date():
            last = n.date()
            try:
                rebuild_styles()
            except Exception as e:
                print("nightly style error:", e, flush=True)
        time.sleep(300)


# ---------- retention ----------
def purge():
    cutoff = (datetime.now(timezone.utc) - timedelta(days=RETENTION_DAYS)).isoformat(timespec="seconds")
    with lock, db() as c:
        files = [r["path"] for r in c.execute("SELECT path FROM audio WHERE created < ?", (cutoff,))]
        n = {t: c.execute(f"DELETE FROM {t} WHERE created < ?", (cutoff,)).rowcount
             for t in ("corrections", "messages", "dictations", "audio")}
    for f in files:
        try:
            os.remove(f)
        except OSError:
            pass
    if any(n.values()):
        print(f"retention ({RETENTION_DAYS} days): removed {n}", flush=True)


def purge_loop():
    while True:
        try:
            purge()
        except Exception as e:  # keep the service alive
            print("purge error:", e, flush=True)
        time.sleep(6 * 3600)


def delete_audio(c, user):
    n = c.execute("DELETE FROM audio WHERE user=?", (user,)).rowcount
    shutil.rmtree(os.path.join(AUDIO_DIR, user), ignore_errors=True)
    return n


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
        """User from the key (set by the gateway). A household key ("*") acts as the
        user picked in the app (X-VK-As), which must be on the server's list."""
        user = (self.headers.get("X-VK-User") or "").strip()
        device = (self.headers.get("X-VK-Device") or "").strip()
        self.household = user == "*"
        if self.household:
            want = (self.headers.get("X-VK-As") or "").strip().lower()
            user = want if any(u["user"] == want for u in users()) else ""
        return user, device

    def no_user(self):
        if getattr(self, "household", False):
            return self.reply(400, {"error": "select a user: send header X-VK-As with a name from GET vk/users"})
        return self.reply(401, {"error": "no user"})

    def raw_body(self, limit):
        n = int(self.headers.get("Content-Length") or 0)
        if n > limit:
            raise ValueError("body too large")
        return self.rfile.read(n) if n else b""

    def body(self):
        return json.loads(self.raw_body(256 * 1024) or b"{}")

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
        if path == "users" and (user or self.household):
            return self.reply(200, {"users": users()})
        if not user:
            return self.no_user()
        with db() as c:
            if path == "me":
                name = next((u["name"] for u in users() if u["user"] == user), user)
                return self.reply(200, {"user": user, "name": name, "device": device,
                                        "household": self.household})
            if path == "profile":
                return self.reply(200, profile(c, user, (q.get("app") or [None])[0],
                                               (q.get("category") or [None])[0]))
            if path == "stats":
                return self.reply(200, stats(c, user, int((q.get("days") or ["30"])[0])))
            if path == "learned":
                v = [{"id": f"v{r['id']}", "word": r["word"], "count": r["count"], "last_seen": r["last_seen"]}
                     for r in c.execute("SELECT * FROM vocabulary WHERE user=? ORDER BY count DESC", (user,))]
                r_ = [{"id": f"r{r['id']}", "from": r["src"], "to": r["dst"], "count": r["count"],
                       "active": r["count"] >= PROMOTE_AFTER, "last_seen": r["last_seen"]}
                      for r in c.execute("SELECT * FROM replacements WHERE user=? ORDER BY count DESC", (user,))]
                a = [{"id": f"a{r['id']}", "word": r["word"], "count": r["count"],
                      "active": r["count"] >= PROMOTE_AFTER, "last_seen": r["last_seen"]}
                     for r in c.execute("SELECT * FROM avoid WHERE user=? ORDER BY count DESC", (user,))]
                s = [dict(r) for r in c.execute("SELECT category, style, n_messages, updated FROM styles WHERE user=?",
                                                (user,))]
                return self.reply(200, {"vocabulary": v, "replacements": r_, "avoid": a, "styles": s})
            if path == "export":
                out = {}
                for t in ("corrections", "messages", "dictations", "replacements", "vocabulary", "avoid",
                          "styles", "meta"):
                    out[t] = [dict(r) for r in c.execute(f"SELECT * FROM {t} WHERE user=?", (user,))]
                out["audio"] = [{k: r[k] for k in ("id", "dictation_id", "text", "bytes", "audio_ms", "created")}
                                for r in c.execute("SELECT * FROM audio WHERE user=?", (user,))]
                return self.reply(200, out)
        self.reply(404, {"error": "not found"})

    def do_POST(self):
        path, q = self.route()
        user, device = self.ident()
        if not user:
            return self.no_user()
        if path == "audio":
            return self.post_audio(user, q)
        if path == "style/rebuild":
            threading.Thread(target=rebuild_styles, args=(user,), daemon=True).start()
            return self.reply(202, {"ok": True, "note": "rebuilding in the background (about a minute)"})
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
        if path == "dictations":
            raw, cleaned, final = clean(b.get("raw"), MAX_MSG), clean(b.get("cleaned"), MAX_MSG), clean(b.get("final"), MAX_MSG)
            if not (raw or cleaned):
                return self.reply(400, {"error": "raw or cleaned text required"})
            low = b.get("low_confidence") if isinstance(b.get("low_confidence"), list) else []
            timings = b.get("timings") if isinstance(b.get("timings"), dict) else {}
            try:
                audio_ms = int(b.get("audio_ms") or 0)
            except (TypeError, ValueError):
                audio_ms = 0
            with lock, db() as c:
                cur = c.execute(
                    "INSERT INTO dictations(user, client_id, device, app, category, lang, stt_model, raw, cleaned, final,"
                    " audio_ms, low_conf, timings, created) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                    (user, clean(str(b.get("id") or ""), 64), device, clean(b.get("app"), 128),
                     clean(b.get("category"), 16) or "other", clean(b.get("lang"), 8), clean(b.get("stt_model"), 128),
                     raw, cleaned, final, audio_ms, json.dumps([w for w in low if isinstance(w, str)][:30]),
                     json.dumps(timings)[:2000], now()))
                learned = learn_dictation(c, user, raw, cleaned, final, low)
                if learned:
                    bump(c, user)
            return self.reply(200, {"ok": True, "id": cur.lastrowid, "learned": learned})
        self.reply(404, {"error": "not found"})

    def post_audio(self, user, q):
        ctype = (self.headers.get("Content-Type") or "").split(";")[0].strip().lower()
        ext = AUDIO_TYPES.get(ctype)
        if not ext:
            return self.reply(415, {"error": f"unsupported audio type {ctype!r}"})
        try:
            data = self.raw_body(MAX_AUDIO)
        except ValueError:
            return self.reply(413, {"error": "audio too large (max 20 MB)"})
        if len(data) < 1000:
            return self.reply(400, {"error": "audio too small"})
        ref = (q.get("dictation") or [""])[0]
        with lock, db() as c:
            d = c.execute("SELECT id, final, cleaned, audio_ms FROM dictations WHERE user=? AND (client_id=? OR id=?) "
                          "ORDER BY id DESC LIMIT 1", (user, ref, ref if ref.isdigit() else -1)).fetchone()
            if not d:
                return self.reply(404, {"error": "unknown dictation: POST vk/dictations first and pass its id"})
            folder = os.path.join(AUDIO_DIR, user)
            os.makedirs(folder, exist_ok=True)
            path = os.path.join(folder, f"{d['id']}.{ext}")
            with open(path, "wb") as f:
                f.write(data)
            c.execute("DELETE FROM audio WHERE user=? AND dictation_id=?", (user, d["id"]))
            c.execute("INSERT INTO audio(user, dictation_id, path, text, bytes, audio_ms, created) VALUES(?,?,?,?,?,?,?)",
                      (user, d["id"], path, d["final"] or d["cleaned"], len(data), d["audio_ms"], now()))
        return self.reply(200, {"ok": True, "dictation": d["id"], "bytes": len(data)})

    def do_DELETE(self):
        path, _ = self.route()
        user, _d = self.ident()
        if not user:
            return self.no_user()
        with lock, db() as c:
            if path == "data":
                counts = {t: c.execute(f"DELETE FROM {t} WHERE user=?", (user,)).rowcount
                          for t in ("corrections", "messages", "dictations", "replacements", "vocabulary", "avoid",
                                    "styles", "meta")}
                counts["audio"] = delete_audio(c, user)
                return self.reply(200, {"ok": True, "deleted": counts})
            if path == "audio":
                return self.reply(200, {"ok": True, "deleted": delete_audio(c, user)})
            m = re.fullmatch(r"learned/([vra])(\d+)", path)
            if m:
                table = {"v": "vocabulary", "r": "replacements", "a": "avoid"}[m.group(1)]
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
    threading.Thread(target=nightly_loop, daemon=True).start()
    print(f"vk-learn on :8082, db {DB}, retention {RETENTION_DAYS} days, style model {STYLE_MODEL} "
          f"nightly at {STYLE_HOUR_UTC}:00 UTC", flush=True)
    ThreadingHTTPServer(("0.0.0.0", 8082), H).serve_forever()
