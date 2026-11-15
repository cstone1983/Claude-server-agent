# Voice Keyboard review, 2026-11-15: prompt for the app chat

Server-side state at the time: all healthy, 29 dictations, 0 errors in 24 h, cleanup 1-2 s typical.
Server fixes already deployed: clip length now measured from the WAV itself (finding 5 can't zero the
training total any more); setup QR now carries `user_id` (finding 9).

Paste this into the voice-keyboard chat:

```
Review of the app against the voice server (2026-11-15). Server side is healthy (no errors in 24 h,
cleanup 1-2 s typical). Please fix these in order; each has file:line on branch
claude/confident-davinci-7bv787 (paths under app/src/main/kotlin/dev/patrickgold/florisboard/).

1. HIGH - likely cause of "previous transcription appended at the end": chunked dictation sends the
   last 200 chars of earlier text + learned vocab/style/custom words as each chunk's Whisper prompt
   (dictate/DictateController.kt ~3580) and appends the chunk result unchecked (~3665). Whisper
   hallucinates the prompt back on quiet/short chunks. Fix: drop a chunk result that mostly repeats the
   continuity text or the prompt (normalised word overlap, e.g. >60% of its words already at the end of
   the previous text), and put the continuity text LAST in the prompt (Whisper truncates from the front).

2. HIGH (server cost) - chunk defaults cut at 0.5 s silence after only 3 s (app/AppPrefs.kt ~782-785:
   vk__chunk_min_seconds=3). Each Whisper request costs ~2.5 s fixed on this CPU server, so 3 chunks per
   10 s dictation is slow. Fix: default min 8 s (keep max 15 s, silence 0.5-0.7 s).

3. MEDIUM - chunk retries: up to 5 retries on any error incl. 401/404, nested with client retries, 120 s
   timeout per call (DictateController ~3413). Fix: retry only network/5xx, short per-chunk timeout
   (~20 s), tell the user if a chunk was lost instead of silently dropping words.

4. MEDIUM - learning queue ordering: submitTraining (vk/learning/VkLearning.kt ~383-384) and sendRecord
   (~304/309) enqueue the dictation record and its audio from separate coroutines; audio can be sent
   first -> server 404 "unknown dictation" -> the clip is deleted (~467); the pending check (~453) only
   looks at items already processed. Fix: enqueue record+audio atomically, and when deciding to drop
   audio after a 404, check the whole pending queue for its record (or just retry audio later).

5. MEDIUM - audio_ms/timings are read at send time, not capture time (VkLearning.kt ~285-286): after
   >10 min of editing they go out as 0. (Server now measures WAV length itself, so training minutes are
   safe, but stats/timings still wrong.) Fix: snapshot them when the dictation is captured.

6. MEDIUM - queue keeps every 400 for retry (VkLearning.kt ~467/481), incl. permanent ones like "raw or
   cleaned text required" (training clip with empty transcription -> blocked 7 days). Fix: only retry
   400 "select a user"; drop other 4xx; send raw=final when transcription is empty.

7. MEDIUM - cleanup uses the shared 120 s timeout + retries (DictateController ~5214); with "hold text
   until done" a stuck cleanup shows nothing for up to 2 minutes. Fix: cleanup-only timeout ~8 s, then
   insert the raw text.

8. LOW-MED - with learning OFF the app still calls GET vk/profile?app=<package>&category= hourly per app
   (FlorisImeService.kt ~401 -> VkLearning.kt ~510-530), leaking app usage. Fix: skip when both learning
   switches are off (training screen uploads are fine, they're explicit).

9. LOW - the setup QR "user" is the DISPLAY name ("Chris"/"household"), but the app uses it as the user
   id (app/setup/OwnServerSetup.kt ~327); if the user list fails, X-VK-As: household is saved and every
   learning call gets 400. The server QR now also carries "user_id" (e.g. "chris", or "*" for a
   household key). Fix: use user_id when present; "*" means ask who is using it (GET vk/users);
   treat "user" as display only.

10. LOW - spoken-punctuation normaliser adds a space after any . , ? ! followed by a letter and
    capitalises (vk/learning/VkCleanup.kt ~228-230): breaks "voice.stoneyshome.com", "e.g.", "Node.js".
    Fix: only touch marks the spoken-punctuation conversion inserted (or skip tokens that look like
    URLs/abbreviations).

11. LOW (privacy/security) - learning queue + profile cache (dictated text) live in jetpref_datastore,
    included in cloud backup/device transfer (res/xml/data_extraction_rules.xml): exclude them.
    Cleartext allowed app-wide (AndroidManifest.xml ~67) and the setup form accepts http:// - refuse
    http for non-LAN hosts. SecretBox.seal falls back to storing the key in plaintext if Keystore fails -
    fail closed instead. /v1/models "task" field is ignored (speech models picked by name) - use it.

Confirmed correct, keep as is: all learning endpoints/fields match the server; X-VK-As only on vk/*
calls; response_format=json, no word timestamps, language sent, small.en default (turbo never
preselected); cleanup sends style only in the system prompt, no examples, temp 0.2, bounded tokens,
output check falls back to raw; audio opt-in, Wi-Fi only, 20 MB cap, never from password fields;
120 min fine-tune target matches the server; key in Android Keystore and never logged; no DELETE /api/ps.
Update docs/fork-notes.md with what you change.
```
