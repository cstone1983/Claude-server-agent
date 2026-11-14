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
