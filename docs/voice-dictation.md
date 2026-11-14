# Self-hosted voice dictation ("Wispr Flow" on our own server)

Started 2026-11-14 (user's request). **ON HOLD (2026-11-14): the user wants a
dedicated chat for this project.** State at hold: VM 113 created and started
(booted into the Ubuntu Server installer, nothing installed yet), UDM fixed IP
+ DNS done. Next: user installs Ubuntu + agent, then the server setup below.

## Server: VM 113 "voice"
- 8 cores (cpu type `host` for AVX2), 16 GB RAM, 64 GB on VM ZFS (discard),
  vmbr0 VLAN 10 firewall=1, agent on, onboot. Ubuntu Server 24.04.2 (user
  installs from local:iso/ubuntu-24.04.2-live-server-amd64.iso).
- Backup: added to the nightly PBS job (21:30, pbs-main) 2026-11-14; the
  off-site Google Drive copy syncs the whole datastore, so it is covered too.
- UDM: fixed IP 10.10.11.113 for bc:24:11:a3:10:6e (client "voice"); DNS
  `voice` + `voice.lan.stoneyshome.com` -> 10.10.11.113.
- Plan: Docker with **speaches** (faster-whisper, OpenAI-compatible
  `/v1/audio/transcriptions`) + **Ollama** (`/v1/chat/completions`) behind one
  gateway: `https://voice.stoneyshome.com/v1/` (Cloudflare tunnel) and
  `voice.lan.stoneyshome.com`, one Bearer API key (root-only file), both paths
  on one base URL. Per-VM firewall 113.fw, nightly backup, health checks, speed
  test. Same Whisper can serve HA Assist (Wyoming) later.

## Build log (this chat builds the server; the app is built in the voice-keyboard repo)
- 2026-11-14 Ubuntu 24.04.5, HWE kernel 7.0, root LV extended to 62 GB,
  qemu-guest-agent, ISO detached. Docker 29.9 (official repo).
- /opt/voice/docker-compose.yml: speaches (ghcr.io/speaches-ai/speaches:latest-cpu,
  127.0.0.1:8000, cache /opt/voice/hf-cache owned by uid 1000!), ollama
  (127.0.0.1:11434, /opt/voice/ollama), gateway (nginx, :8080).
- Gateway /opt/voice/gateway/voice.conf: one Bearer key for everything except
  /health; /v1/audio/* + /v1/models -> speaches, /v1/chat/* -> ollama. Key in
  /root/.config/voice/api-key (root-only; nginx include key.conf mode 600).
  Lesson: an nginx `if` inside `location /` does NOT cover nested locations -
  the check must be at server level (verified 401 without key, 2026-11-14).
- cloudflared 2026.11.0 as a service (tunnel "Voice"), route
  voice.stoneyshome.com -> http://localhost:8080. Tunnel token was posted in
  chat: rotate it in Cloudflare.
- Models: Whisper deepdml/faster-whisper-large-v3-turbo-ct2 and
  Systran/faster-distil-whisper-small.en; Ollama qwen2.5:7b and qwen2.5:3b.
- First speed test (11 s JFK sample, 8 vCPU, default settings): small.en
  4.6 s but dropped words; large-v3-turbo ~20 s (too slow - tune: int8,
  threads, VAD, more cores); qwen2.5:3b cleanup 2.4 s, very good.
- Getting the key onto a phone: `sudo voice-key-qr [url|key|app]` on voice
  shows QR codes in the terminal (qrencode). Contents:
  - url: `https://voice.stoneyshome.com/v1/`
  - key: the 64-character lowercase hex key only (no "Bearer", no newline)
  - app: one JSON line for our app's "scan to set up":
    `{"v":1,"base_url":"https://voice.stoneyshome.com/v1/","api_key":"<64 hex>"}`
    The app sends `Authorization: Bearer <api_key>` on every request.
  Later: pairing code + per-device keys.
- Tuning 2026-11-14 (11 s sample): VM now 16 cores, but Whisper fastest at
  8 threads (16 crosses CPU sockets): small.en ~3.6-3.9 s via the API, beam
  size makes no difference; large-v3-turbo ~11-12 s whatever the settings
  (too slow on CPU, no GPU possible). Chosen: speech
  Systran/faster-whisper-small.en, cleanup qwen2.5:3b (~2.5 s). Chunked
  upload in the app (docs/keyboard-chunked-prompt.md) hides most of it.
- Per-request cost (measured 2026-11-14): small.en 3 s clip 2.7 s, 5.5 s
  clip 3.2 s, 11 s clip 4.1 s (about 2.5 s fixed + 0.15 s per audio second);
  turbo ~14 s for ANY length (fixed 30 s window, big encoder). So: chunked
  upload yes with small.en but long chunks (8-15 s); turbo never; a
  WebSocket/streaming endpoint would not help on CPU (cost is the window,
  not HTTP). Told the app chat.
- Split DNS (2026-11-14): UDM local record voice.stoneyshome.com -> 10.10.11.102
  (Traefik on n8n, router "voice-public-inside", own Let's Encrypt cert via
  cfdns) -> voice gateway 10.10.11.113:8080. At home the same address stays
  inside and works during internet outages; away it resolves publicly to the
  Cloudflare tunnel. LAN has no IPv6, so the public AAAA answers fail fast and
  clients use IPv4 (inside). Phone Private DNS must stay Off/Automatic.
  voice-api.lan.stoneyshome.com (Traefik router + UDM record) is now redundant:
  remove after the user confirms. Lesson: edit lan.yml via a temp file + YAML
  check, then mv (a str.replace on "  middlewares:" hit the idrac router).
- Setup QR "app" format now also carries stt_model + chat_model (the app
  pre-fills them). /v1/models lists only the Whisper models (speaches); the
  cleanup model must come from the QR or be typed. Possible later: merge
  Ollama's list into /v1/models.
- To do: 113.fw firewall (observe -> block), voice.lan via
  Traefik, health checks.

## Phone: keyboard research (2026-11-14)
- **Top pick: Dictate** (net.devemperor.dictate, Play ~US$3.49, Apache-2.0,
  github.com/DevEmperor/Dictate): full FlorisBoard-based keyboard; "own server"
  provider for transcription AND rewording (base URL ending `/v1/`, API key,
  model from /v1/models); rewording prompts with "apply automatically" + prompt
  buttons; hold/toggle, cancel, auto-enter, silence skip, custom words,
  replacements, language switch. Samsung: only DeX/hardware-keyboard issue.
- Runner-up: Voice Keyboard (com.tyraen.voicekeyboard, F-Droid, MIT): voice-only
  IME, silence auto-stop, send-after-pause, hint field, post-processing modes.
- Not chosen: Polished Recognition (single prompt), Whisper to Input (no
  cleanup), VocaPhone (own protocol only), FUTO (on-device only), HeliBoard mic
  (launches a voice IME; behaviour unconfirmed).

## Idea: our own keyboard app (user, 2026-11-14)
User may want to build our own Android keyboard for this setup instead of
Dictate. Notes for when we pick it up:
- Starting points (open source, could fork rather than start from zero):
  Dictate (Apache-2.0, FlorisBoard-based, already speaks OpenAI-compatible
  transcription + rewording), FlorisBoard (Apache-2.0 keyboard base),
  HeliBoard (GPL-3.0, AOSP-based).
- Things only our own app could do: talk to voice.stoneyshome.com with our
  key built in, per-app cleanup styles (casual in messages, formal in email),
  hooks into Home Assistant / n8n (e.g. "note this" -> Nextcloud, voice
  commands from the keyboard), personal vocabulary synced from the server,
  streaming partial results.
- Decide after a few weeks of using Dictate: build only for what it lacks.

### Learning feature (user, 2026-11-14)
Phase 2 design + app prompt, ready to build: docs/voice-learning.md

The keyboard gets better the more it is used; everything stays on our server.
- Capture corrections: after dictated text is inserted, notice what the user
  edits (original vs. final text) and send the pair to the server.
- Step 1, cheap and immediate:
  - personal vocabulary (names, places, project words) fed to Whisper as its
    hint/prompt;
  - automatic replacement rules for repeated mishears;
  - a "my style" profile + recent examples given to the cleanup model (tone,
    punctuation, emoji habits, sign-offs), per app if wanted.
- Step 2, later: fine-tune the cleanup model (small LoRA on Ollama-compatible
  models) on accumulated corrections; optionally fine-tune Whisper on the
  user's own voice clips (needs stored audio, opt-in).
- Guard rails: opt-in, never learn from password/secure fields, a "forget"
  button, per-app exclusions, data in one place that is backed up and easy to
  wipe.
- Dictate already has custom words + replacements (manual); the learning loop
  (automatic) is the main reason to build our own app.

### First-run "training session" (user, 2026-11-14)
~5 minutes together when the user first sets it up, to give it a solid base.
1. Read-aloud script (~2-3 min), generated for this household, not generic:
   family member names (read at run time from Home Assistant `person.*`
   entities - never stored in this public repo), places, server and
   device names (Proxmox, n8n, Obi-Wan, Lutron, ...), HA room/device names,
   numbers/dates/times, addresses and phone-number formats, plus sentences
   with tricky punctuation ("new line", lists, questions).
2. Free talk (~2 min): answer a few prompts naturally ("describe your day",
   "text <a family member> that you're running late") to capture real phrasing and style.
3. Server compares Whisper output to the script ->
   - words it got wrong become vocabulary hints + fix rules immediately;
   - picks the best model size for this voice (accuracy vs. speed, measured);
   - sets silence/auto-stop timing from the user's natural pauses;
   - free-talk text seeds the "my style" profile for the cleanup AI.
4. Shows a before/after score (word error rate) so the gain is visible; can be
   re-run any time (new phone mic, new names).
Honest limit: 5 minutes is enough for vocabulary, settings and style, which is
where most of the gain is. Actually retraining Whisper on the user's voice
needs much more audio (tens of minutes to hours) collected over time, opt-in.
Can be done before our own app exists: a small web page on voice.lan that
records in the browser, runs the script and builds Dictate's custom-words +
replacement list to import.
