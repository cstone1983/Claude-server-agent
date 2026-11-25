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
                                "cleanup_style","cleanup_avoid","style_examples","drop_endings"}
                            drop_endings: 1-3 word sentences the person deleted 2+ times at the
                            end of a chunk/dictation (stray Whisper endings, e.g. "Kama") - strip them
  GET    stats              -> {"days":[...], "totals":{...}, "audio":{...}}
  GET    learned            -> {"vocabulary":[...],"replacements":[...],"avoid":[...]}
  DELETE learned/<id>       (id like "v12", "r3" or "a5")
  POST   style/rebuild      rebuild the writing-style summaries now (normally nightly)
  DELETE audio              delete all stored audio clips of this user
  GET    export             -> everything stored for this user (audio as file list)
  DELETE data               -> forget everything for this user (audio included)
  GET    health             (no user needed)
"""
import difflib, io, json, os, re, shutil, sqlite3, sys, threading, time, urllib.request, wave
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
STT = os.environ.get("VK_STT", "http://speaches:8000")         # Whisper server (for per-person models)
MAX_PERSONAL = 4 * 1024 * 1024     # vk/personal profile body limit (app doc: "up to about 4 MB")
PROMOTE_AFTER = 2          # identical correction / removal seen this often -> active
AVOID_AFTER = 3            # a word cleanup added and the user removed this often -> cleanup_avoid
SIMILAR = 0.6              # a correction only teaches when before/after are this similar (letters)
DROP_AFTER = 2             # a 1-3 word sentence deleted this often at a chunk/dictation end -> drop_endings
DICT_FILE = os.environ.get("VK_DICT", "/app/words.txt")   # English word list (lowercase entries = dictionary words)
MAX_EDITS, MAX_LEN, MAX_MSG, MAX_AUDIO = 20, 200, 4000, 20 * 1024 * 1024
VOCAB_CAP, PROMPT_CHARS = 100, 250   # Whisper prompt: the app adds ~180 chars of its own + ~200 of previous text
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
        CREATE TABLE IF NOT EXISTS personal(
          user TEXT PRIMARY KEY, body TEXT NOT NULL, bytes INTEGER, updated_at INTEGER NOT NULL,
          created TEXT NOT NULL);
        """)


def migrate():
    """Columns added after the first release."""
    with db() as c:
        cols = {r[1] for r in c.execute("PRAGMA table_info(dictations)")}
        if "source" not in cols:     # "training" = voice-training program (scripted text), else dictation
            c.execute("ALTER TABLE dictations ADD COLUMN source TEXT")


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


def load_dictionary():
    """Lowercase entries of an English word list (capitalised entries are names, not
    'everyday words'). Falls back to COMMON if the list is missing."""
    try:
        with open(DICT_FILE, encoding="utf-8", errors="ignore") as f:
            words = {w.strip() for w in f if w.strip() and w[0].islower()}
        words = {w.replace("’", "'") for w in words if not w.endswith("'s")}   # skip possessive forms
        print(f"dictionary: {len(words)} words from {DICT_FILE}", flush=True)
        return words | COMMON
    except OSError:
        print(f"dictionary: {DICT_FILE} missing - only the small COMMON list is used", flush=True)
        return set(COMMON)


DICTIONARY = None


def is_dictionary_word(w):
    global DICTIONARY
    if DICTIONARY is None:
        DICTIONARY = load_dictionary()
    w = w.lower().replace("’", "'")
    return w in DICTIONARY or (w.endswith("s") and w[:-1] in DICTIONARY)


LETTERS = re.compile(r"[^a-z]")


def letters(s):
    return LETTERS.sub("", s.lower())


def similar(before, after):
    """Is this a correction of what was heard (similar letters), not a rewrite?
    'Clod'->'Claude', 'at Glow, and'->'aGlow' yes; 'Here is'->'Give me', 'dog'->'talk' no.
    A single spoken word -> symbols/number ('slash'->'/') also counts."""
    b, a = letters(before), letters(after)
    if not a:
        return len(before.split()) == 1 and bool(after.strip())
    if not b:
        return False
    if difflib.SequenceMatcher(None, b, a).ratio() >= SIMILAR:
        return True
    bw, aw = before.split(), after.split()
    return len(bw) == len(aw) and all(
        letters(x) and letters(y) and difflib.SequenceMatcher(None, letters(x), letters(y)).ratio() >= SIMILAR
        for x, y in zip(bw, aw))


NAME_WORD = re.compile(r"[A-Za-z][A-Za-z'’-]+")


def name_like(w, after, context):
    """Capitalised in the middle of a sentence, or not an everyday dictionary word.
    Plain words only (no domains like 'mine.lol', no digits)."""
    if not NAME_WORD.fullmatch(w) or len(w) < 3 or w.lower() in COMMON:
        return False
    if not is_dictionary_word(w):
        return True
    if not w[0].isupper():
        return False
    first = WORD.findall(after)[:1] == [w]
    left = (context or "").split("___")[0].rstrip()
    sentence_start = first and (not left or left[-1] in ".!?\n")
    return not sentence_start


def add_vocab(c, user, word, t):
    if len(word) >= 2 and word.lower() not in COMMON:
        c.execute("INSERT INTO vocabulary(user, word, count, last_seen) VALUES(?,?,1,?) "
                  "ON CONFLICT(user, word) DO UPDATE SET count = count + 1, last_seen = excluded.last_seen",
                  (user, word, t))
        return True
    return False


def learn_edit(c, user, before, after, context=""):
    """Turn one correction into learned items. Returns True if anything changed.
    Every correction is stored (stats); only SIMILAR ones (a misheard word, not a
    rewrite) become replacements, and a word only reaches the vocabulary (= the
    Whisper prompt) once its replacement is promoted or when it is name-like."""
    before, after = strip_shared_punct(before, after)
    if not before or not after or before == after:
        return False                       # pure insertions/deletions (e.g. removed filler) teach nothing
    if re.fullmatch(r"[\d\s.,:/-]+", before + after):
        return False                       # number/time formatting, not vocabulary
    if len(before.split()) > 6 or len(after.split()) > 6:
        return False                       # too big to be a single correction
    if all_common(before) and all_common(after):
        return False                       # grammar-style fix of everyday words: needs context, no blind rule
    if not similar(before, after):
        return False                       # a rewrite ("Here is" -> "Give me"), not a correction
    t = now()
    c.execute("INSERT INTO replacements(user, src, dst, count, last_seen) VALUES(?,?,?,1,?) "
              "ON CONFLICT(user, src, dst) DO UPDATE SET count = count + 1, last_seen = excluded.last_seen",
              (user, before, after, t))
    count = c.execute("SELECT count FROM replacements WHERE user=? AND src=? AND dst=?",
                      (user, before, after)).fetchone()[0]
    before_words = {w.lower() for w in WORD.findall(before)}
    for w in WORD.findall(after):
        if w.lower() not in before_words and (count >= PROMOTE_AFTER or name_like(w, after, context)):
            add_vocab(c, user, w, t)
    return True


def ending_key(before, context):
    """If this deletion looks like a stray Whisper ending - a 1-3 word sentence on its
    own at the end of a chunk/dictation - return it without punctuation, else None."""
    b = (before or "").strip()
    if not b or b[-1] not in ".!?" or not (1 <= len(b.split()) <= 3):
        return None
    right = (context or "").partition("___")[2].strip()
    if right and not right[0].isupper():
        return None                        # the sentence continues after it: a real edit
    key = b.strip(EDGE_PUNCT)
    if not key or all_common(key):
        return None                        # never strip everyday phrases like "Come on."
    return key


def drop_endings(c, user):
    counts, forms = {}, {}
    for r in c.execute("SELECT before, context FROM corrections WHERE user=? AND coalesce(after,'')=''", (user,)):
        k = ending_key(r["before"], r["context"])
        if k:
            counts[k.lower()] = counts.get(k.lower(), 0) + 1
            forms.setdefault(k.lower(), k)
    return [forms[k] for k, n in sorted(counts.items(), key=lambda x: -x[1]) if n >= DROP_AFTER]


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
            # never everyday words: telling cleanup to avoid "it" or "for" breaks sentences
            if w not in raw_w and w not in final_w and len(w) >= 2 and w not in COMMON:
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
        "SELECT word FROM avoid WHERE user=? AND count >= ? ORDER BY count DESC LIMIT 30", (user, AVOID_AFTER))
        if r["word"] not in COMMON]
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
            "cleanup_style": safe_style(style["style"]) if style else "",
            # style_examples kept for compatibility but always empty: full example
            # messages in the cleanup prompt got copied into new dictations.
            "cleanup_avoid": avoid, "category": category or "", "style_examples": [],
            # stray Whisper endings this person keeps deleting (e.g. "Kama"); the app strips them
            "drop_endings": drop_endings(c, user)}


def stats(c, user, days=30):
    since = (datetime.now(timezone.utc) - timedelta(days=days)).isoformat(timespec="seconds")
    # Accuracy per dictation: what was inserted (cleaned) vs. what the user finally
    # sent (final). Only dictations with a known final text count.
    per = {}
    for r in c.execute("SELECT substr(created,1,10) d, cleaned, final, audio_ms FROM dictations "
                       "WHERE user=? AND created>=?", (user, since)):
        d = per.setdefault(r["d"], {"day": r["d"], "dictations": 0, "audio_s": 0, "words": 0,
                                    "corrected_words": 0, "corrections": 0})
        d["dictations"] += 1
        d["audio_s"] += round((r["audio_ms"] or 0) / 1000)
        if r["final"]:
            d["words"] += len(words(r["cleaned"]))
            d["corrected_words"] += corrected_words(r["cleaned"], r["final"])
    for r in c.execute("SELECT substr(created,1,10) d, count(*) n FROM corrections WHERE user=? AND created>=? "
                       "GROUP BY d", (user, since)):
        per.setdefault(r["d"], {"day": r["d"], "dictations": 0, "audio_s": 0, "words": 0,
                                "corrected_words": 0, "corrections": 0})["corrections"] = r["n"]
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
Describe HOW this person writes - never WHAT they write about - as at most 5 short bullet points
of general traits that a text-cleanup assistant can respect when tidying their dictation:
tone and formality, sentence length, emoji use, how formal or casual openings and closings are.
Rules: describe general traits only. NEVER quote or list any words, phrases or expressions from
the messages, and never give example sentences. Never mention topics, projects, technologies,
products, apps, people or places. Do not describe their interests. Do not give punctuation or
grammar advice that would make the text less correct. Reply with the bullet points only."""

# A style line must describe a trait, never carry wording the cleanup could copy
# into a message (that happened: quoted favourite phrases got appended).
STYLE_LINE_BAD = re.compile(r"[\"“”‘’']|e\.g\.|for example|such as|phras|expression|like to say|words like",
                            re.IGNORECASE)


def safe_style(text):
    lines = [l.strip() for l in text.splitlines() if l.strip()]
    return "\n".join([l for l in lines if not STYLE_LINE_BAD.search(l)][:5])


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
            style = safe_style(style)            # traits only, max 5, no quoted wording
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


_stt_cache = {"t": 0.0, "ids": set()}


def stt_models():
    """Model ids the Whisper server can serve right now (cached for a minute)."""
    if time.time() - _stt_cache["t"] > 60:
        try:
            with urllib.request.urlopen(STT + "/v1/models", timeout=5) as r:
                _stt_cache["ids"] = {m.get("id") for m in json.load(r).get("data", []) if m.get("id")}
            _stt_cache["t"] = time.time()
        except Exception:
            pass        # keep the last known list
    return _stt_cache["ids"]


def personal_stt_model(user, available=None):
    """The person's own fine-tuned Whisper model (installed by train-voice.sh as
    local/whisper-small.en-<user>) once the Whisper server can serve it, else ""."""
    ids = stt_models() if available is None else available
    want = f"local/whisper-small.en-{user}"
    if want in ids:
        return want
    mine = sorted(i for i in ids if i.startswith("local/") and i.endswith(f"-{user}"))
    return mine[0] if mine else ""


def unix(iso):
    try:
        return int(datetime.fromisoformat(iso).timestamp())
    except (TypeError, ValueError):
        return 0


def seed_personal(c, user, cap=3000):
    """A starting profile for a person without one: lowercase word -> [uses, newest use]
    from their stored dictations and messages. {} when there is nothing to seed from."""
    counts = {}
    rows = list(c.execute("SELECT coalesce(nullif(final,''), cleaned, raw) t, created FROM dictations WHERE user=?",
                          (user,))) + list(c.execute("SELECT text t, created FROM messages WHERE user=?", (user,)))
    for r in rows:
        ts = unix(r["created"])
        for w in WORD.findall(r["t"] or ""):
            w = w.lower().strip(".'’-")
            if not w:
                continue
            n, last = counts.get(w, (0, 0))
            counts[w] = (n + 1, max(last, ts))
    if not counts:
        return {}
    top = sorted(counts.items(), key=lambda kv: (-kv[1][0], kv[0]))[:cap]
    return {"version": 1, "updated_at": 0, "seeded": True, "words": {w: [n, t] for w, (n, t) in top}}


class H(BaseHTTPRequestHandler):
    server_version = "vk-learn"

    def reply(self, code, obj):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def reply_raw(self, code, body):
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
            self.discard(n)
            raise ValueError("body too large")
        return self.rfile.read(n) if n else b""

    def discard(self, n, cap=64 * 1024 * 1024):
        """Read and drop an oversized body so the client gets a clean 413 instead of a
        broken connection mid-upload (the gateway already caps bodies at 50 MB)."""
        if n > cap:
            self.close_connection = True
            return
        while n > 0:
            chunk = self.rfile.read(min(n, 1024 * 1024))
            if not chunk:
                break
            n -= len(chunk)

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
                out = {"user": user, "name": name, "device": device, "household": self.household}
                m = personal_stt_model(user)
                if m:
                    out["stt_model"] = m       # app sends it as `model`; transcriptions fall back if it fails
                return self.reply(200, out)
            if path == "personal":
                r = c.execute("SELECT body, updated_at FROM personal WHERE user=?", (user,)).fetchone()
                if r:
                    prof = json.loads(r["body"])
                    prof["updated_at"] = r["updated_at"]          # the server's time is the version
                    return self.reply(200, prof)
                return self.reply(200, seed_personal(c, user))   # never 404: the app reads that as "no endpoint"
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
                r = c.execute("SELECT body, updated_at FROM personal WHERE user=?", (user,)).fetchone()
                out["personal"] = {"updated_at": r["updated_at"], "profile": json.loads(r["body"])} if r else None
                return self.reply(200, out)
        self.reply(404, {"error": "not found"})

    def do_POST(self):
        path, q = self.route()
        user, device = self.ident()
        if not user:
            return self.no_user()
        if path == "audio":
            return self.post_audio(user, q)
        if path == "personal":
            return self.post_personal(user)
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
                    context = clean(e.get("context"), MAX_LEN)
                    learned += learn_edit(c, user, before, after, context)
                    if not after.strip() and ending_key(before, context):
                        learned += 1               # may change drop_endings: bump the profile version
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
                    " audio_ms, low_conf, timings, created, source) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                    (user, clean(str(b.get("id") or ""), 64), device, clean(b.get("app"), 128),
                     clean(b.get("category"), 16) or "other", clean(b.get("lang"), 8), clean(b.get("stt_model"), 128),
                     raw, cleaned, final, audio_ms, json.dumps([w for w in low if isinstance(w, str)][:30]),
                     json.dumps(timings)[:2000], now(), clean(b.get("source"), 16) or "dictation"))
                learned = learn_dictation(c, user, raw, cleaned, final, low)
                if learned:
                    bump(c, user)
            return self.reply(200, {"ok": True, "id": cur.lastrowid, "learned": learned})
        self.reply(404, {"error": "not found"})

    def post_personal(self, user):
        """Store the person's whole personalisation profile (format version 1), replacing
        the old one. The app merges before posting; unknown fields are kept as sent."""
        try:
            data = self.raw_body(MAX_PERSONAL)
        except ValueError:
            return self.reply(413, {"error": "profile too large (max 4 MB)"})
        try:
            prof = json.loads(data or b"{}")
        except Exception:
            return self.reply(400, {"error": "invalid JSON"})
        if not isinstance(prof, dict):
            return self.reply(400, {"error": "profile must be a JSON object"})
        # Several devices per person: optimistic concurrency. "if_updated_at" is the updated_at the
        # device merged from (0 / the seed's time when it saw no stored profile); never stored.
        has_if = "if_updated_at" in prof
        want = prof.pop("if_updated_at", None)
        try:
            want = int(want) if want is not None else None
        except (TypeError, ValueError):
            return self.reply(400, {"error": "if_updated_at must be unix seconds"})
        with lock, db() as c:
            r = c.execute("SELECT updated_at FROM personal WHERE user=?", (user,)).fetchone()
            if has_if and r and r["updated_at"] != want:
                return self.reply(409, {"error": "changed", "updated_at": r["updated_at"]})
            t = int(time.time())
            if r and t <= r["updated_at"]:
                t = r["updated_at"] + 1      # always moves forward, so a stale if_updated_at never matches
            prof["updated_at"] = t           # the server's time is the profile's version
            body = json.dumps(prof, ensure_ascii=False, separators=(",", ":"))
            c.execute("INSERT INTO personal(user, body, bytes, updated_at, created) VALUES(?,?,?,?,?) "
                      "ON CONFLICT(user) DO UPDATE SET body=excluded.body, bytes=excluded.bytes, "
                      "updated_at=excluded.updated_at", (user, body, len(body.encode()), t, now()))
        return self.reply(200, {"ok": True, "updated_at": t})

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
            # The clip's own length counts (the app's audio_ms can be 0 or stale).
            ms = wav_ms(data) if ext == "wav" else None
            ms = ms or d["audio_ms"] or 0
            if ms and not d["audio_ms"]:
                c.execute("UPDATE dictations SET audio_ms=? WHERE id=?", (ms, d["id"]))
            c.execute("DELETE FROM audio WHERE user=? AND dictation_id=?", (user, d["id"]))
            c.execute("INSERT INTO audio(user, dictation_id, path, text, bytes, audio_ms, created) VALUES(?,?,?,?,?,?,?)",
                      (user, d["id"], path, d["final"] or d["cleaned"], len(data), ms, now()))
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
                                    "styles", "meta", "personal")}
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


def wav_ms(data):
    """Length of a WAV clip in ms, or None if it can't be read."""
    try:
        with wave.open(io.BytesIO(data)) as w:
            return int(w.getnframes() * 1000 / w.getframerate()) if w.getframerate() else None
    except Exception:
        return None


def backfill_audio_ms():
    """Measure stored WAV clips whose length was recorded as 0 (older app builds)."""
    n = 0
    with lock, db() as c:
        for r in c.execute("SELECT id, dictation_id, path FROM audio WHERE coalesce(audio_ms,0)=0").fetchall():
            try:
                with open(r["path"], "rb") as f:
                    ms = wav_ms(f.read()) if r["path"].endswith(".wav") else None
            except OSError:
                ms = None
            if ms:
                c.execute("UPDATE audio SET audio_ms=? WHERE id=?", (ms, r["id"]))
                c.execute("UPDATE dictations SET audio_ms=? WHERE id=? AND coalesce(audio_ms,0)=0", (ms, r["dictation_id"]))
                n += 1
    if n:
        print(f"backfilled the length of {n} audio clip(s)", flush=True)


def clean_learned(remove_vocab=()):
    """One-off clean-up after the 2026-11-25 learning fixes: everyday words out of
    'avoid', rewrites out of 'replacements', the given words out of 'vocabulary'.
    The raw corrections stay (stats)."""
    with lock, db() as c:
        avoid = [r["word"] for r in c.execute("SELECT DISTINCT word FROM avoid") if r["word"] in COMMON]
        for w in avoid:
            c.execute("DELETE FROM avoid WHERE word=?", (w,))
        reps = [r for r in c.execute("SELECT id, src, dst FROM replacements") if not similar(r["src"], r["dst"])]
        for r in reps:
            c.execute("DELETE FROM replacements WHERE id=?", (r["id"],))
        vocab = [w for w in remove_vocab if c.execute("DELETE FROM vocabulary WHERE word=?", (w,)).rowcount]
        for r in c.execute("SELECT DISTINCT user FROM corrections").fetchall():
            bump(c, r["user"])
    print("avoid: removed everyday words", avoid)
    print("replacements: removed rewrites", [f"{r['src']!r}->{r['dst']!r}" for r in reps])
    print("vocabulary: removed", vocab)


if __name__ == "__main__" and "--clean" in sys.argv:
    init(); migrate()
    clean_learned(sys.argv[sys.argv.index("--clean") + 1:])
    sys.exit(0)

if __name__ == "__main__":
    init()
    migrate()
    backfill_audio_ms()
    threading.Thread(target=purge_loop, daemon=True).start()
    threading.Thread(target=nightly_loop, daemon=True).start()
    print(f"vk-learn on :8082, db {DB}, retention {RETENTION_DAYS} days, style model {STYLE_MODEL} "
          f"nightly at {STYLE_HOUR_UTC}:00 UTC", flush=True)
    ThreadingHTTPServer(("0.0.0.0", 8082), H).serve_forever()
