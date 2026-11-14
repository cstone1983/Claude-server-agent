# Prompt for the app chat: transcribe while the user is still talking

Paste everything between the lines into the voice-keyboard chat.

---

## Feature: chunked transcription while speaking (lower latency)

**Why:** the voice server runs Whisper on CPU only (no GPU possible). Today
the app records everything, then uploads it, so the user waits for the
whole clip to be transcribed after they stop talking: about 5-6 s for 11 s
of speech with `Systran/faster-whisper-small.en`, plus about 2.5 s for the
cleanup step. Goal: when the user stops talking, most of the audio is
already transcribed, so only the last few seconds plus cleanup remain.

**Server (already running, no changes needed for this feature):**
- Base URL `https://voice.stoneyshome.com/v1/`, header
  `Authorization: Bearer <api_key>` on every request (401 without it).
- Speech-to-text: `POST {base}audio/transcriptions` (OpenAI-compatible,
  multipart: `file`, `model`, optional `language`, `prompt`,
  `response_format`). WAV is tested and works. Compressed formats
  (OGG/Opus, M4A) should work too, because the server decodes audio with
  PyAV/FFmpeg, but test one before relying on it. Prefer 16 kHz mono.
- Cleanup: `POST {base}chat/completions` (OpenAI-compatible), model
  `qwen2.5:3b`.
- `GET https://voice.stoneyshome.com/health` returns `ok` without a key.
- The server handles several requests at once, but each Whisper request
  uses all CPU cores, so send chunks one after another (a queue), not in
  parallel bursts.

**How it should work:**
1. Record 16 kHz mono PCM. Run on-device voice activity detection (Silero
   VAD via ONNX Runtime, or WebRTC VAD; both are small and permissively
   licensed). Check the licence before adding a dependency.
2. Cut a chunk at a natural pause: at least ~0.5 s of silence after at
   least ~3 s of speech, or force a cut at ~15 s (cut at the quietest point
   of the last second, never in the middle of a word). Keep about 0.2-0.3 s
   of overlap or padding at the edges so words are not clipped.
3. Upload each chunk as soon as it is cut, in order, through a queue with
   one request in flight. Encode it (WAV is fine on Wi-Fi; Opus saves mobile
   data). Retry a failed chunk once and keep order by sequence number.
4. Pass the text of the previous chunk(s) (last ~200 characters) in the
   `prompt` field. Whisper uses it as context, which keeps punctuation,
   names and casing consistent across chunks. Also add the user's custom
   vocabulary there.
5. Show the partial text live in the keyboard's preview strip as chunks
   come back, but don't insert it into the text field yet (or insert it as
   a composing/underlined span that can still be replaced).
6. When the user stops: cut the last chunk immediately, wait for it, join
   all chunk texts with single spaces, drop exact duplicated words at chunk
   edges caused by the overlap, then run the cleanup request ONCE on the
   full joined text (not per chunk), and insert the result.
7. Show elapsed timing in a debug/settings screen: per-chunk transcription
   time, time from "stop" to final text. We want "stop to text" under about
   3 s for a typical 10-20 s message.
8. Settings: chunked mode on/off (fallback: the current record-then-upload
   mode), silence length, max chunk length, model names.
9. Edge cases: no speech at all (don't send anything), user cancels
   (abort the queue and discard), network loss mid-recording (keep chunks
   locally, retry, or fall back to sending the whole recording when back),
   very long dictation (minutes) must not run out of memory.

**Done when:** on the user's Samsung Galaxy Fold, a 15-20 s dictation shows
partial text while speaking, and the final cleaned-up text appears within
about 3 s of stopping. Measure and report: stop-to-text time in chunked
vs. non-chunked mode, for 3 dictations each.

**Not in scope:** server changes. If you think a server change would help
(for example a streaming/WebSocket endpoint), describe it and ask; the server
is managed in a separate chat (Claude-server-agent repo,
docs/voice-dictation.md).

---
