# Prompt: build our own voice keyboard (paste into a new chat)

Copy everything between the lines into a new Claude Code session on the
Claude-server-agent repository (with the Server Agent Hub and Home Assistant
connectors attached).

---

## Project: a self-hosted "Wispr Flow" Android keyboard with learning

You are picking up a project that was planned in an earlier chat. Read these
first, in this order, and treat them as the source of truth:
1. `docs/voice-dictation.md` - the plan, the keyboard research, the learning
   feature and the first-run training session.
2. `NOTES.md` - home-lab state, rules, and where secrets live
   ("Secrets and where they live"), the per-VM firewall method, and the
   `.lan.stoneyshome.com` / Cloudflare tunnel setup.
3. `docs/internal-https.md` - how Traefik on the n8n VM, the `cfdns` resolver
   and the `*.lan.stoneyshome.com` names work.

### Goal
An Android keyboard for the user's phone (Samsung Galaxy Fold, One UI) that:
- types normally (a real keyboard: layout, swipe, predictions, autocorrect),
- dictates through the user's OWN server (Whisper speech-to-text + a local AI
  model that cleans the text up, like Wispr Flow: no filler words, fixed
  grammar, sensible punctuation and formatting, the user's own tone),
- works in every app and from anywhere (public HTTPS through the existing
  Cloudflare tunnel, protected by an API key),
- learns from the user over time (see "Learning" below),
- starts with a ~5 minute guided training session.

### Work in phases; finish and verify each before the next
Stop at the end of every phase, show the user what works, and ask before going
on.

**Phase 0 - server (prerequisite; skip what is already done).**
VM 113 "voice" exists on the Proxmox host (8 cores, CPU type `host` for AVX2,
16 GB RAM, 64 GB disk, Servers VLAN, fixed IP 10.10.11.113, DNS
`voice.lan.stoneyshome.com`). Check its state first (`qm config 113`,
`list_servers`). If Ubuntu or the agent is missing, ask the user to install
them. Then:
- updates, guest agent, Docker;
- **speaches** (faster-whisper, OpenAI-compatible `/v1/audio/transcriptions`)
  and **Ollama** (OpenAI-compatible `/v1/chat/completions`);
- one gateway so both answer under one base URL `https://voice.stoneyshome.com/v1/`
  (and `https://voice.lan.stoneyshome.com/v1/` at home), one Bearer API key
  kept in a root-only file (never printed in chat or committed);
- the per-VM firewall (`/etc/pve/firewall/113.fw`, observe first, then block,
  same method as the other VMs in NOTES.md), nightly backup, and the scheduled
  health checks;
- measure speed and accuracy with the user's voice; pick the Whisper model
  (e.g. large-v3-turbo vs. smaller) and the cleanup model from measurements,
  not guesses.
Verify with a real request from outside the LAN before Phase 1.

**Phase 1 - pick the base and build an MVP keyboard.**
- Do not write a keyboard from scratch. Compare forking **Dictate**
  (github.com/DevEmperor/Dictate, Apache-2.0, FlorisBoard-based, already
  speaks OpenAI-compatible transcription + rewording) with **FlorisBoard**
  (Apache-2.0) and **HeliBoard** (GPL-3.0). Recommend one with reasons
  (license, code quality, how much already exists, maintenance) and wait for
  the user's choice.
- The keyboard code gets its **own GitHub repository** (not this one; this
  repo is public and holds home-lab tooling). The user creates the repo; then
  attach it to the session. Respect the original license and keep attribution.
- MVP: normal typing + mic key → record → send to `/v1/audio/transcriptions`
  → optional cleanup via `/v1/chat/completions` → insert text. Settings:
  server URL, API key (stored with Android's encrypted storage, never logged),
  models, language (auto or fixed), hold-to-talk / tap-to-toggle, silence
  auto-stop, auto-send, cleanup on/off, cleanup presets with editable prompts
  (e.g. "clean up", "formal", "casual", "translate").
- Build tooling: try to build the APK in the session. If the Android SDK or
  Gradle downloads are blocked by the environment's network rules, read the
  environment documentation, say which hosts are needed, and fall back to a
  GitHub Actions workflow in the new repo that builds and signs the APK.
- Delivery: a signed APK the user sideloads (e.g. via Nextcloud or a GitHub
  release). Keep the signing key out of git (GitHub Actions secret).

**Phase 2 - learning (all data stays on the user's server).**
- After dictated text is inserted, detect the user's edits in that field and
  send (original, final) pairs to the server (opt-in).
- Server builds: a personal vocabulary sent to Whisper as its hint/prompt,
  automatic fix rules for repeated mishears, and a "my style" profile with
  recent examples for the cleanup model, optionally per app (casual in
  messages, formal in email).
- Guard rails: off by default; never learn from password/secure input fields
  or apps on an exclusion list; a "forget everything" button; data in one
  place on the server, backed up, easy to wipe.
- Later (only when there is enough data, and the user agrees): fine-tune the
  cleanup model on corrections; optional Whisper tuning on the user's own
  voice clips (needs stored audio, explicit opt-in).

**Phase 3 - first-run training session (~5 minutes).**
- 2-3 minutes reading a script generated for this household: family member
  names (read at run time from Home Assistant `person.*` entities), places,
  server/device/room names (from HA and NOTES.md), numbers, dates, times,
  addresses, phone numbers, lists and punctuation.
- 2 minutes of free talk on a few prompts, to capture real phrasing and style.
- Result: mistakes become vocabulary + fix rules, the best model size and the
  silence timing are chosen from measurements, the free talk seeds the style
  profile, and a before/after word-error-rate score is shown. Can be re-run.
- Can ship earlier as a web page on `voice.lan.stoneyshome.com` if the app is
  not ready.

**Phase 4 - nice to have (ask first).**
Streaming partial results while speaking; voice commands to Home Assistant or
n8n from the keyboard (e.g. "note this" → Nextcloud); per-app styles; Wear OS.

### Rules (from the home lab; follow them)
- Never paste secrets in chat; use root-only files or stdin. Never commit
  secrets. Never put personal data (family names, addresses, contacts) in the
  public repo; the keyboard repo should also stay free of them.
- Server changes: verify before acting, one change at a time, verify after.
  Never shut down or reboot the Proxmox host. Do not delete VMs. Run
  `update_server` only when the user says.
- Follow the per-VM firewall method in NOTES.md; Samba stays LAN-only.
- Keep `docs/voice-dictation.md` updated with status and decisions; commit and
  push docs changes on the working branch.
- Don't include AI model identifiers in commits, code comments or docs.
- The user prefers short, clear answers, and decisions as numbered options
  with a recommendation.

### First message to the user
Summarize what you found (Phase 0 state: VM 113, server pieces present or
not), list the phases with rough effort, and ask which base to fork after a
short comparison.

---
