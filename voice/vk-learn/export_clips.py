#!/usr/bin/env python3
"""export_clips.py <user> <learn.db> <data dir> <out clips.jsonl>

Builds the Whisper fine-tune dataset for one person from vk-learn's stored clips.

The label must be what was SPOKEN, not the text that was finally sent:
- Dictations: start from `raw` (Whisper's own transcript of the clip) and apply only the person's
  own word corrections (the cleaned -> final edits, word-aligned onto raw). Cleanup's rewording is
  NOT taken over. Clips where cleanup changed more than punctuation, capitals or filler words
  (word distance between that label and `final` > 20 %) are skipped: raw is then too unsure.
- Voice-training program clips (source = training, scripted sentences): `final` is the script the
  person read, used as is.
- Clips over 30 s are skipped: Whisper only trains on the first 30 s, but the label would cover all.
Prints how many clips were kept and why the others were skipped.
"""
import difflib, json, os, re, sqlite3, sys, wave
from collections import Counter

MAX_MS = 30_000
MAX_DIST = 0.20
MAX_EDIT_WORDS = 4          # a correction touching more words than this is a rewrite, not a fix
MAX_INSERT_WORDS = 2        # words the person added that Whisper may have missed
FILLERS = {"um", "uh", "uhm", "umm", "uhh", "erm", "er", "ah", "eh", "hmm", "hm", "mm", "mhm"}


def norm(tok):
    t = tok.lower().replace("’", "'")
    return re.sub(r"^[^\w']+|[^\w']+$", "", t)


def words(text):
    return (text or "").split()


def content(text):
    """Normalised words without punctuation, case or fillers (for the distance check)."""
    return [n for n in (norm(t) for t in words(text)) if n and n not in FILLERS]


def wer(a, b):
    """Word edit distance a -> b, relative to len(b)."""
    if not b:
        return 0.0 if not a else 1.0
    prev = list(range(len(b) + 1))
    for i, x in enumerate(a, 1):
        cur = [i]
        for j, y in enumerate(b, 1):
            cur.append(min(prev[j] + 1, cur[j - 1] + 1, prev[j - 1] + (x != y)))
        prev = cur
    return prev[-1] / len(b)


def apply_corrections(raw, cleaned, final):
    """raw with the person's own cleaned->final word edits applied. Returns (label, problem)."""
    r, c, f = words(raw), words(cleaned), words(final)
    nr, nc, nf = [norm(t) for t in r], [norm(t) for t in c], [norm(t) for t in f]
    c2r = {}
    for tag, i1, i2, j1, j2 in difflib.SequenceMatcher(None, nc, nr, autojunk=False).get_opcodes():
        if tag == "equal":
            for k in range(i2 - i1):
                c2r[i1 + k] = j1 + k
    edits = []      # (r_start, r_end, new tokens)
    for tag, i1, i2, j1, j2 in difflib.SequenceMatcher(None, nc, nf, autojunk=False).get_opcodes():
        if tag == "equal":
            continue
        if max(i2 - i1, j2 - j1) > MAX_EDIT_WORDS:
            return None, "person rewrote part of the text"
        new = f[j1:j2]
        if tag == "insert":
            if j2 - j1 > MAX_INSERT_WORDS:
                return None, "person added text"
            if i1 - 1 in c2r:
                at = c2r[i1 - 1] + 1
            elif i1 in c2r:
                at = c2r[i1]
            elif not c:
                at = len(r)
            else:
                return None, "correction not alignable to raw"
            edits.append((at, at, new))
            continue
        idx = [c2r.get(i) for i in range(i1, i2)]
        if None in idx or idx != list(range(idx[0], idx[0] + len(idx))):
            return None, "correction not alignable to raw"
        edits.append((idx[0], idx[-1] + 1, new if tag == "replace" else []))
    for s, e, new in sorted(edits, key=lambda x: x[0], reverse=True):
        r[s:e] = new
    return " ".join(r), None


def wav_ms(path):
    try:
        with wave.open(path) as w:
            return int(w.getnframes() * 1000 / w.getframerate())
    except Exception:
        return 0


def label_for(row):
    """(label, skip reason) for one clip row."""
    if (row["source"] or "") == "training":
        return ((row["final"] or "").strip() or None), (None if (row["final"] or "").strip() else "empty text")
    raw, cleaned, final = row["raw"] or "", row["cleaned"] or "", row["final"] or ""
    if not raw.strip():
        return None, "empty raw transcript"
    if not final.strip():
        return None, "empty final text"
    label, problem = apply_corrections(raw, cleaned or raw, final)
    if problem:
        return None, problem
    if wer(content(label), content(final)) > MAX_DIST:
        return None, "cleanup changed the wording (>20 %)"
    return label, None


def main():
    user, dbfile, data, out = sys.argv[1:5]
    c = sqlite3.connect(f"file:{dbfile}?mode=ro", uri=True)
    c.row_factory = sqlite3.Row
    cols = {r[1] for r in c.execute("PRAGMA table_info(dictations)")}
    src = "d.source" if "source" in cols else "NULL"
    rows = c.execute(f"SELECT a.id, a.path, a.audio_ms, d.id dict_id, d.raw, d.cleaned, d.final, {src} source "
                     "FROM audio a JOIN dictations d ON d.id = a.dictation_id WHERE a.user=? ORDER BY a.id",
                     (user,)).fetchall()
    kept, why, kept_ms = [], Counter(), 0
    for row in rows:
        path = row["path"].replace("/data/", data.rstrip("/") + "/", 1)
        ms = row["audio_ms"] or wav_ms(path)
        if not os.path.exists(path):
            why["audio file missing"] += 1; continue
        if not ms:
            why["length unknown"] += 1; continue
        if ms > MAX_MS:
            why["longer than 30 s"] += 1; continue
        label, reason = label_for(row)
        if reason:
            why[reason] += 1; continue
        kept.append({"audio": path, "text": label, "dictation": row["dict_id"], "ms": ms,
                     "source": row["source"] or "dictation"})
        kept_ms += ms
    with open(out, "w") as fh:
        for k in kept:
            fh.write(json.dumps(k, ensure_ascii=False) + "\n")
    print(f"{len(rows)} clips: kept {len(kept)} ({kept_ms / 60000:.1f} min), skipped {len(rows) - len(kept)}")
    for reason, n in why.most_common():
        print(f"  skipped {n:4d}: {reason}")
    print(f"written: {out}")


if __name__ == "__main__":
    main()
