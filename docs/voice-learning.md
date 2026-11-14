# Voice dictation - Phase 2: learning from corrections

Prepared 2026-11-14 for the next session. Server side is built in this repo's
chat (VM 113 "voice"); app side in the voice-keyboard repo's chat.

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

## Server design (to build)
- New small service `vk-learn` (Python, FastAPI or plain stdlib) in
  /opt/voice/docker-compose.yml, listening only inside the compose network.
  Gateway routes `/v1/vk/` to it (same Bearer key as everything else).
- Storage: SQLite at /opt/voice/learn/learn.db (on the VM disk, so in the
  nightly PBS backup). No audio stored.
- Endpoints (JSON):
  - `POST /v1/vk/corrections` - body:
    ```json
    {"device":"<random id per install>","app":"<package name>","lang":"en",
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

Send:
- POST {base}vk/corrections with
  {"device","app","lang","stt_model","edits":[...],"ts"}; queue and retry
  offline; drop after 7 days.
- Style learning is a SEPARATE opt-in: POST {base}vk/style with the final
  text, app package and category (chat/email/other).

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
