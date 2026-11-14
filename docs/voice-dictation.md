# Self-hosted voice dictation ("Wispr Flow" on our own server)

Started 2026-11-14 (user's request). Status: VM created, waiting for the user
to install Ubuntu + the agent.

## Server: VM 113 "voice"
- 8 cores (cpu type `host` for AVX2), 16 GB RAM, 64 GB on VM ZFS (discard),
  vmbr0 VLAN 10 firewall=1, agent on, onboot. Ubuntu Server 24.04.2 (user
  installs from local:iso/ubuntu-24.04.2-live-server-amd64.iso).
- UDM: fixed IP 10.10.11.113 for bc:24:11:a3:10:6e (client "voice"); DNS
  `voice` + `voice.lan.stoneyshome.com` -> 10.10.11.113.
- Plan: Docker with **speaches** (faster-whisper, OpenAI-compatible
  `/v1/audio/transcriptions`) + **Ollama** (`/v1/chat/completions`) behind one
  gateway: `https://voice.stoneyshome.com/v1/` (Cloudflare tunnel) and
  `voice.lan.stoneyshome.com`, one Bearer API key (root-only file), both paths
  on one base URL. Per-VM firewall 113.fw, nightly backup, health checks, speed
  test. Same Whisper can serve HA Assist (Wyoming) later.

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
