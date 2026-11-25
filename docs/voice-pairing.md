# Voice Keyboard Wi-Fi pairing (server side)

Contract: voice-keyboard repo, docs/server-pairing.md (main). Built 2026-11-25.

- **mDNS:** avahi on the voice VM (allow-interfaces=ens18), /etc/avahi/services/voicekb.service
  (repo: voice/pair/voicekb.service.xml): "Voice server", _voicekb._tcp, port 8080,
  TXT path=/v1/ name=Voice server. The UDM's mDNS reflector carries it from Servers to Main
  (checked from 10.10.1.185: answer via 10.10.1.1).
- **Gateway** (voice/gateway/voice.conf): /v1/vk/pair, /v1/vk/pair/info and
  /v1/vk/pair/<id> need no key but only answer when the real client address is in
  10.10.1.0/24 (Main) and there is no CF-Connecting-IP / X-Forwarded-For / Forwarded header.
  The tunnel (cloudflared on the VM -> localhost:8080) arrives as 172.18.0.1 and Traefik as
  10.10.11.102: both get 403. Rate limits per address: info + new requests 5/min (burst 4),
  status polling 30/min (the app polls every 3 s). Everything else under /v1/vk/pair -> 404.
- **voice-pair** (voice/pair/voice-pair.py -> /opt/voice/pair/, systemd voice-pair.service,
  root, listens 172.18.0.1:8083 = Docker bridge only). Uses /usr/local/sbin/voice-user's own
  functions, so keys appear in `voice-user list` with the device name and can be revoked.
  At most 3 requests waiting at once; request_id = 32 random bytes; 4-digit code; expire after
  10 min; approved setup handed out once then deleted (404); denied/expired answer for a day.
  New person: id from the name (lowercase letters/digits), made unique.
- **Telegram:** a dedicated bot (the hub's @Stoney_servers_bot is long-polled by csa-hub and
  can't be shared). Token: /root/.config/voice/pair-bot.token (root-only). Owner chat:
  /root/.config/voice/pair.conf chat_id=6897458189 (both the chat and the sender must match).
  Without a token, POST /v1/vk/pair answers 503 "approval by the owner is not set up".
- Logs: journalctl -u voice-pair (no keys, codes or tokens are logged).

## Verified 2026-11-25 (from Ubuntu-Anthony, 10.10.1.185 on Main)

- mDNS query from Main answered via the UDM reflector (10.10.1.1): Voice server, path=/v1/, :8080.
- info lists the people; POST -> 202 + Telegram alert from @Stoneys_voice_bot (code, device, LAN
  address, Approve/Deny); approve -> setup with a new 64-char personal key (user_id chris) exactly
  once, then 404; key k2 shown in `voice-user list` with its device name (revoked after the test);
  deny (new person) -> "denied", person not added; 6th quick info call -> 429.
- Refused (403): public URL, Traefik LAN route, LAN with X-Forwarded-For, localhost (tunnel path).
- Bot token saved through a one-time LAN-only page (never via chat), checked with getMe.
