# Voice dictation - Phase 2: learning from corrections

Prepared 2026-11-14 for the next session. Server side is built in this repo's
chat (VM 113 "voice"); app side in the voice-keyboard repo's chat.

## Decisions (user, 2026-11-15)
1. Users: only "chris" for now (current key becomes chris / Fold7).
2. Store final messages from the start (style learning and a message
   database built up early) - still an opt-in switch in the app.
3. Keep raw corrections and stored messages for 1 year (365 days); learned
   vocabulary and fix rules stay until deleted.

## Status (2026-11-15): SERVER SIDE BUILT AND TESTED
- `voice-user` (/usr/local/sbin, source voice/voice-user): users.tsv + keys.tsv in
  /root/.config/voice (root-only); `sync` writes the gateway map
  /opt/voice/gateway/key.conf IN PLACE (single-file bind mount!) with
  $voice_user / $voice_device / $voice_key_id / $voice_auth_ok and reloads nginx.
  Existing key migrated: user chris, device Fold7, id k1.
- `vk-learn` (container voice-learn, python:3.12-alpine, source
  voice/vk-learn/learn.py, DB /opt/voice/learn/learn.db, retention 365 d):
  gateway route /v1/vk/ sets X-VK-User/X-VK-Device from the key (client
  values overwritten - verified a spoofed X-VK-User is ignored).
- Tested through the gateway: me, corrections (2x same fix -> rule),
  messages, profile, learned, delete, export, forget-everything. Test data
  removed. Works inside (split DNS via n8n) and via Cloudflare.
- Setup QR (`sudo voice-key-qr` = `voice-user qr k1`) now also carries
  "user" and "device" (display only).
- Name picker (user, 2026-11-15): key k1 is a HOUSEHOLD key (user "*" in
  keys.tsv): it may act as any user on the list; the app gets the names from
  GET vk/users and sends the selected one as header X-VK-As (must be on the
  list, else 400). Personal keys ignore X-VK-As. Names-only list for the
  service: /opt/voice/vk-learn/users.tsv (voice-user writes it; compose mounts
  the vk-learn DIRECTORY). Later: Google sign-in or personal keys before
  sharing outside the family (anyone with the household key can pick any name).
- To do: health-check line for voice-learn; nightly style summary
  (cleanup_style) later.

## Goal
The dictation gets better from the user's own corrections, automatically,
with everything stored only on the user's server.

## How it works (end to end)
1. **App (keyboard)** inserts dictated text and remembers exactly what it
   inserted. While the user edits that text in the same field, it re-reads the
   field; at the end point (Send/enter action, field cleared, field left or
   keyboard closed) it compares inserted vs. final text.
2. If only part changed (small edits, not a rewrite), the app sends the
   **changes** (with a few words of context) to the server.
3. **Server** stores them and builds:
   - **vocabulary**: words/names the user corrects to (e.g. Jesse -> Jessy);
   - **fix rules**: the same mishear fixed the same way 2+ times becomes an
     automatic replacement;
   - **style** (separate opt-in): recent final texts per app category, as
     examples for the cleanup model.
4. **App** fetches the profile and applies it: vocabulary goes into Whisper's
   `prompt` field, fix rules run on the transcription, style examples go into
   the cleanup system prompt.

## Per-person profiles (user's request, 2026-11-14)
Learning belongs to a **person**, not to an install or a phone, so a reinstall
or a new phone keeps everything learned.
- Server keeps **users** (e.g. `chris`), each with one or more **API keys**
  (one per device, so a lost phone's key can be revoked without affecting the
  others). All learning data (corrections, vocabulary, rules, style) is stored
  per **user**.
- The gateway maps the key to the user (nginx `map` of key -> username,
  generated from a root-only keys file) and passes it to the services as a
  header `X-VK-User`; a client-sent `X-VK-User` is always overwritten, so the
  identity only ever comes from the key. Unknown key -> 401 as today.
- The app never chooses the user: the key decides. After a reinstall the user
  scans their setup QR again (same key, or a fresh key for the same user) and
  gets their profile back. `GET /v1/vk/me` returns `{"user":"chris",
  "display_name":"Chris","device":"Fold7"}` so the app can show "Signed in as
  Chris".
- Setup QR gains `"user"` (display only) - the key is what counts.
- Admin on the server: `sudo voice-user add <name>`, `sudo voice-user key
  <name> [--device Fold7]` (prints the setup QR in the terminal),
  `sudo voice-user revoke <key-id>`, `sudo voice-user list`.
- Other family members can get their own user, so each person's vocabulary and
  style stay separate.
- The current single key becomes user "chris", device "Fold7" at migration.

## Server design (to build)
- New small service `vk-learn` (Python, FastAPI or plain stdlib) in
  /opt/voice/docker-compose.yml, listening only inside the compose network.
  Gateway routes `/v1/vk/` to it (same Bearer key as everything else).
- Storage: SQLite at /opt/voice/learn/learn.db (on the VM disk, so in the
  nightly PBS backup), every row keyed by user (from `X-VK-User`). No audio
  stored.
- Endpoints (JSON):
  - `POST /v1/vk/corrections` - body:
    ```json
    {"device":"<install id, info only>","app":"<package name>","lang":"en",
     "stt_model":"...","edits":[{"before":"Jesse","after":"Jessy",
     "context":"meet ___ at 8:30"}],"ts":"2026-11-15T10:00:00Z"}
    ```
    Returns `{"ok":true}`. Server re-validates (length limits, max edits).
  - `POST /v1/vk/style` (only if style learning is on) - body
    `{"device":..., "app":..., "category":"chat|email|other", "text":"<final text>"}`.
    Keeps the last N (e.g. 30) per category.
  - `GET /v1/vk/profile?app=<package>` - returns:
    ```json
    {"version":12,
     "vocabulary":["Jessy","Proxmox","n8n"],
     "replacements":[{"from":"Jesse","to":"Jessy","count":3}],
     "whisper_prompt":"Jessy, Proxmox, n8n.",
     "cleanup_style":"Casual. Short sentences. Rarely uses emoji. Signs off with -C.",
     "style_examples":["...","..."]}
    ```
    `whisper_prompt` is ready to pass as Whisper's `prompt` (kept under ~200
    tokens). `version` changes when anything changed (app can cache).
  - `GET /v1/vk/learned` / `DELETE /v1/vk/learned/<id>` - list and remove
    single learned items (app shows an editable "Learned words" list).
  - `DELETE /v1/vk/data` - forget everything (all tables), returns counts.
  - `GET /v1/vk/export` - everything as JSON (for the user).
- Rules: a replacement is promoted after 2 identical corrections; ignore
  edits where more than ~40 % of words changed (rewrite, not a correction);
  ignore digits-only changes; max 200 chars per edit; vocabulary capped (most
  recent/frequent ~100 words).
- Later: nightly job that summarizes style ("cleanup_style") with the local
  model from the stored examples; optional fine-tuning (opt-in).
- Monitoring: add to the health checks (service up, DB size).

## App prompt (paste into the voice-keyboard chat)

```text
## Phase 2: learn from the user's corrections (server API ready from the server chat)

Goal: dictation improves from the user's own edits, stored only on the user's
server (same base URL + Bearer key as transcription).

Capture (in the keyboard):
1. When dictated text is inserted, remember the inserted text, the field
   (app package + field id) and its position.
2. While the keyboard stays attached to that field, re-read the text around
   the dictated span (InputConnection getTextBeforeCursor/AfterCursor or
   getExtractedText) on edits.
3. End point: the enter/Send action (IME action SEND/GO/DONE), the field being
   cleared right after, onFinishInput, or the keyboard closing. Take the final
   version of the dictated span then.
4. Diff inserted vs. final at word level. Ignore if >40 % of words changed
   (a rewrite) or nothing changed. Produce small edits
   {"before","after","context"} with context = up to 3 words each side, the
   changed words replaced by "___".
5. Never capture from password/secure fields (inputType password variants,
   no-personalized-learning flag, IME_FLAG_NO_PERSONALIZED_LEARNING), or apps
   on the user's exclusion list. Never read or send text outside the
   dictated span (except the small context).

Identity: learning is per PERSON, decided by the API key (the server maps
key -> user). Don't invent a user id in the app. After setup/reinstall call
GET {base}vk/me and show "Signed in as <display_name>". The setup QR may
contain "user" (display only).

Send:
- POST {base}vk/corrections with
  {"device","app","lang","stt_model","edits":[...],"ts"}; queue and retry
  offline; drop after 7 days.
- Message storing ("Learn my writing style") is a SEPARATE opt-in: POST
  {base}vk/messages {"app","category":"chat|email|other","text","lang"}
  with the final sent text (the user wants this on and building up early).
  Stored 1 year on the server.

Use:
- GET {base}vk/profile?app=<package> on keyboard start and at most hourly
  (cache by "version").
- Put "whisper_prompt" (plus the previous-chunk context) into the Whisper
  "prompt" field.
- Apply "replacements" to the transcription before cleanup (whole words,
  case-preserving).
- Add "cleanup_style" and up to 3 "style_examples" to the cleanup system
  prompt.

Settings screen "Learning":
- Learn from my corrections (default OFF), Learn my writing style (default
  OFF), excluded apps list, "Learned words" (GET {base}vk/learned, swipe to
  delete via DELETE {base}vk/learned/<id>), "Export", "Forget everything"
  (DELETE {base}vk/data, confirm first).

Done when: correcting the same mishear twice makes the third dictation come
out right, and "Forget everything" empties the learned list.
```

## Next: Google sign-in (user plans it, 2026-11-15)
Replace the household key + name picker with "Sign in with Google":
- Google Cloud project "Voice Keyboard"; OAuth consent screen External in
  TESTING mode (allowed accounts added as test users, no Google review).
- OAuth clients: Web application (its Client ID = token audience the server
  checks; no secret needed) and Android (package
  io.github.cstone1983.voicekeyboard + SHA-1 of the signing cert, from the app
  chat's CI keystore).
- App: Credential Manager "Sign in with Google" -> ID token -> POST
  vk/auth/google once -> server verifies (Google JWKS signature, aud = web
  client ID, iss, exp, email_verified) and checks the email allow-list
  (email -> user) -> issues a personal per-device key (voice-user key ...)
  returned to the app; app stores it (Keystore) and drops the picker.
- Server to build: the endpoint, allow-list file (root-only), key issuing and
  revoking, then turn the household key k1 personal or revoke it.

## Next learning step: context-aware (planned 2026-11-15)
Today's rules are blind word swaps ("Jesse" -> "Jessy" everywhere). Plan, once
1-2 weeks of real corrections/messages exist:
- Conflict detection: if stored final messages also KEEP the original spelling
  (e.g. both "Jesse" and "Jessy" are real names), don't promote a blind rule.
- Context-gated rules: use the stored context words of each correction; a rule
  fires only when the surrounding words resemble the past fixes.
- Let the cleanup model decide ambiguous names: profile gains
  "names_in_context" (both spellings + example sentences) for the cleanup
  prompt.
- Spoken punctuation ("comma", "period", "new line") is NOT a learning task:
  fixed conversion in the app (being built by the app chat, 2026-11-15),
  plus a backup line in the cleanup prompt.
- Server fix 2026-11-15: everyday-word fixes (this->the, there->their: both
  sides only common English words) are stored but never become rules or
  vocabulary; vocabulary excludes common words. Removed the This->The rule.
- Server fixes already done 2026-11-15: punctuation around corrected words is
  stripped (Jesse, -> Jessy, stored as Jesse -> Jessy); a bogus rule from the
  cleanup "text" bug was removed.

## Learning from how the user speaks (user's goal, 2026-11-15)
Beyond corrections, layered:
1. Writing style from stored final messages: nightly job summarizes
   (local model) into profile.cleanup_style, per app category. Server only.
2. Speaking habits: app sends per dictation {raw whisper text, cleaned text,
   final sent text, app, lang, timings}; server learns phrase habits, spoken
   commands ("smiley face", "sign it Chris"), what the user undoes from cleanup,
   low-confidence words -> Whisper hints. Needs app change.
3. Voice adaptation (opt-in): keep audio clips + final corrected text as
   training pairs; after a few hours, fine-tune faster-whisper small.en on the
   user's voice (overnight on CPU). Needs audio storage + delete button.
4. Metrics: daily accuracy (corrected words per dictation) to show progress.
Suggested order: 1+4 (server now), 2 (app+server), 3 later.
