# Self-hosted app store with automatic updates - PLAN (not built; awaiting approval)

Drafted 2026-11-21 for implementation Monday 2026-11-23. Nothing below has been changed yet.

## 0. Push-back first

1. **Simplest alternative: GitHub Releases.** Your app repos already live on GitHub and their CI
   already builds APKs. Obtainium supports GitHub releases natively (token for private repos,
   pre-release filter). That reaches the same goal with zero new infrastructure. The self-hosted
   route below is better only if you want builds, signing key and releases off GitHub, private
   releases without GitHub, and no dependence on GitHub. I plan the self-hosted route as asked;
   say if you'd rather take the GitHub route.
2. **"Claude never handles the keystore" is a policy, not a technical guarantee,** if Claude can
   change any workflow that runs with the signing secrets: a workflow can print or upload a secret
   in disguise. The design below makes it technical: the secrets live only in ONE builder repo
   that only you can change, and the app's own code never runs while the key is present (build
   and sign are separate jobs).
3. **Existing installs must be reinstalled once.** Apps installed today over wireless debugging
   are signed with other keys (debug keys or GitHub CI keys). Android refuses an update signed
   with a new key, and silent updates also require that Obtainium did the first install. So each
   app is uninstalled once and reinstalled through Obtainium, which **loses its local data**:
   export the SSH Sync vault first; Voice Keyboard needs its server QR again (its learning lives
   on the server). Alternative: reuse ONE existing release key as the shared keystore so that one
   app keeps its installs; the others still reinstall once.
4. **Disk I/O is the host's real bottleneck, not CPU.** Proxmox shows CPU pressure 0 but I/O
   pressure 20-31 % (some task waiting on disk). Android/Gradle builds are I/O-heavy. Before
   Monday's build I will find what causes it (per-VM I/O; needs one Proxmox read approval) and
   limit the build VM's disk bandwidth so builds cannot slow down HA, voice or the hub.

## 1. VM and resources

Host now: 48 threads (2x E5-2680 v3), load ~20 (cpu-share's miner uses ~18.5 threads), CPU
pressure 0; RAM 188 GB, 65 GB available (ZFS ARC holds 34 GB); ZFS pool "VM" 3.8 TB free.

**Recommendation: one new VM, Forgejo + runner together.** VM 114 "forge", 10.10.11.114 (servers
VLAN), Ubuntu 24.04:
- 8 vCPU, **cpuunits lower than the other VMs** so builds yield CPU to everything else.
- 16 GB RAM (Forgejo ~0.5 GB; a Flutter/Gradle build peaks at 6-10 GB).
- Disk 1: 60 GB system + Forgejo data (in PBS backup).
- Disk 2: 120 GB build cache (Gradle, Flutter, Android SDK, Docker images), **excluded from
  backup**, with a bandwidth limit (MB/s and IOPS) on this disk only.
- Runner capacity 1 (one build at a time), with CPU and memory limits on the job containers, so
  Forgejo stays responsive during builds.

Why not a separate runner VM now: at one person's scale it doubles patching, backups and
monitoring for little gain. Split it out later if builds get frequent, or when the Windows
runner arrives (that needs its own Windows machine anyway).

Why not an existing VM: Vm-finance carries apps plus the standby hub, voice is latency-sensitive,
and n8n is the hub and Traefik. Builds there would hurt them.

**Forgejo rather than Gitea:** same API (Obtainium's source is literally "Forgejo (Codeberg)"),
community-governed, built-in Actions. No reason to pick Gitea. Runs as the official Docker image
with SQLite (enough for one user; simple backups).

## 2. Build runner

- forgejo-runner (Docker) on VM 114, label `android`. Jobs run in containers.
- A prebuilt **builder image** (Flutter + Android SDK + JDK + build tools at pinned versions) is
  built once and stored in Forgejo's own container registry, so jobs don't download ~5 GB each
  time. Gradle and pub caches live on disk 2.
- The runner never gets the Docker socket inside jobs.

## 3. Where the code comes from

Claude Code sessions run in the cloud and **cannot reach a LAN-only Forgejo**, so **GitHub stays
the source of truth** for app code.
- One Forgejo org `apps`:
  - one **release repo** per app (`apps/voice-keyboard`, `apps/ssh-sync`, ...), private, holding
    only releases (what Obtainium watches);
  - one **builder repo** `apps/builder`, private, with the build workflows and the signing secrets.
    **Only you can change it** (branch protection: you alone can push or merge to main).
- A build is started by `workflow_dispatch` (inputs: app, git ref). Claude starts it through the
  hub agent on VM 114, calling Forgejo's local API. Nothing about building is exposed publicly.
  - Optional later: a scheduled job that builds new tags on GitHub automatically.
- A Forgejo account `claude-bot` can dispatch builds and read logs. It **cannot push to the
  builder repo, cannot see secrets, and cannot promote releases**.

## 4. Example workflow outline (`apps/builder/.forgejo/workflows/android.yml`; no code yet)

1. **Trigger:** `workflow_dispatch(app, ref)`.
2. **Job "build"** (container: builder image, no secrets):
   - check out the app from GitHub at `ref`;
   - set versionName from the tag/commit, and versionCode from a per-app counter that only goes up
     (starting above any versionCode already installed);
   - run `flutter build apk --release`, producing an **unsigned** APK, plus tests;
   - upload the APK as an artifact.
3. **Job "sign-and-publish"** (needs build; plain container; secrets; **no app code checked out**):
   - write the keystore from the secret to tmpfs;
   - `zipalign`, then `apksigner sign`, then `apksigner verify` (checking the certificate hash);
   - delete the keystore;
   - create a **pre-release** `v<version>` in `apps/<app>` with the signed APK, its SHA-256 and the
     commit/changelog;
   - send a Telegram message: "<app> <version> pre-release ready - promote in Forgejo".
4. **Promotion:** you untick "pre-release" in the Forgejo UI (LAN). Obtainium has "Include
   prereleases" OFF, so phones only see it after that.
   - Optional later: a Telegram "Promote" button.

## 5. Secrets layout (all in `apps/builder` repo secrets, entered by you in the Forgejo UI)

| Secret | What |
|---|---|
| `ANDROID_KEYSTORE_B64` | release keystore, base64 |
| `ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_ALIAS`, `ANDROID_KEY_PASSWORD` | its passwords/alias |
| `RELEASE_TOKEN` | token of a `release-bot` user that may only write releases in `apps/*` |
| `GITHUB_READ_TOKEN` | only if app repos on GitHub become private (fine-grained, read-only) |
| `TELEGRAM_NOTIFY` | optional: n8n server-notify webhook URL+key, or the bot token |

- You generate the keystore on your PC (`keytool -genkeypair ... -keyalg RSA -keysize 4096
  -validity 10000`), keep the file and passwords offline (two copies), and paste them as secrets.
- **Losing the keystore means no app can ever be updated in place again.**
- Obtainium tokens: per app, a token of a `phone-reader` user with **read-only** access to the
  release repos, entered in each app's Obtainium settings.

## 6. Phone access: public release paths only (recommended) vs VPN

**Recommended (a): Cloudflare tunnel with path-limited routes.**
- `apps.stoneyshome.com` routes **only** the paths Obtainium needs:
  - the releases API (`/api/v1/repos/<owner>/<repo>/releases`, `/releases/latest`, `/tags`);
  - release asset downloads (`/<owner>/<repo>/releases/download/...` and `/attachments/...`);
  - everything else gets 404 at Cloudflare.
- The login page, git and admin are never public.
- Repos are private, so even those paths need the read-only token.
- Same address at home and away. The full Forgejo UI is LAN-only at
  `forge.lan.stoneyshome.com` (Traefik on n8n, wildcard cert).
- The tunnel: cloudflared on VM 114 with its own new tunnel (a route on an existing tunnel would
  also work, but would tie downloads to that machine).

**Not recommended as the main path (b): VPN only (WireGuard on the UDM, or Tailscale).**
- Updates would only happen while the VPN is up. Android allows one VPN at a time, and
  battery/always-on VPN quirks would quietly stop the 30-minute checks.
- Good as an add-on later, not as the delivery path.

To verify Monday before relying on it:
- Forgejo accepts Obtainium's `Authorization: Token <t>` header (Forgejo docs couldn't be
  fetched today; I'll test with curl);
- private asset downloads work with that header, and are served directly, not redirected to
  another origin (Obtainium drops the token on cross-origin redirects);
- the Cloudflare path rules let nothing else through.

## 7. Phone setup (once per device)

- **Obtainium:** background update check every 30 min; enable its foreground-service option if
  the Fold's battery manager delays checks.
- **Each app:** add it with Source = "Forgejo (Codeberg)" (Override Source), URL
  `https://apps.stoneyshome.com/apps/<app>`, the read-only token, "Include prereleases" OFF, and
  the expected signing-certificate hash (Obtainium then hard-blocks anything else).
- **Silent updates need each app's `targetSdk` >= the phone's Android API level minus 3.** I'll
  check each app's targetSdk before onboarding.
- **Alternative to those limits:** Obtainium + Shizuku installs silently regardless.

## 8. Backups and monitoring

- **Backups:**
  - VM 114 joins the nightly 21:30 PBS job, and therefore the 23:00 off-site copy; disk 2 is
    excluded.
  - A nightly `forgejo dump` (a consistent snapshot of SQLite + repos + config) is written to
    disk 1 before PBS runs.
  - The keystore is NOT recoverable from these by you on purpose: your offline copy is the only
    source of truth.
- **Monitoring:**
  - The hub agent on VM 114 gives offline, disk-full and failed-service alerts automatically.
  - `vm-health` in the daily check covers its containers.
  - Additional checks: Forgejo `/api/healthz`, runner online, cache disk usage, and failed builds
    (Telegram).

## 9. Monday, step by step

1. (Approval) Proxmox read: find the I/O pressure source; confirm the VM size.
2. Create VM 114 (2 disks, cpuunits, disk I/O limit), firewall file (enforced: SSH + 3000 from
   n8n/LAN, nothing else), DHCP reservation/DNS `forge` + `forge.lan`.
3. You: install Ubuntu from the ISO as before, then the hub agent (I give the command).
4. Updates, Docker, Forgejo (Docker, SQLite), first admin account (**you** set its password),
   Traefik route `forge.lan.stoneyshome.com`.
5. Org `apps`, the users `claude-bot` / `release-bot` / `phone-reader` with their scopes, the
   builder repo with branch protection.
6. Runner + builder image; a dry-run build of one app, unsigned.
7. You: keystore + secrets in the UI. Then the first signed pre-release; check signature +
   versionCode.
8. You: create the tunnel `forge` (token via a root-only file as before), path-limited routes;
   I test from outside (token required, nothing else reachable).
9. Phone: Obtainium setup, uninstall old app, install through Obtainium; promote a test release
   and watch it update silently.
10. Backups (PBS job + forgejo dump), monitoring, notes.

## 10. Later: Windows

- **Builds:** a Windows runner on a Windows VM (needs a Windows licence; ~4 vCPU / 8 GB), or a
  Windows PC you own running forgejo-runner. Flutter can't build Windows apps on Linux.
- **Updates:** publish Velopack packages (`releases.win.json` + `.nupkg`) as assets on the same
  release repos. The app's Velopack updater reads them (via a simple web source pointing at the
  release downloads; Velopack's Gitea/Forgejo source to be verified then), through the same
  token.
- **Code signing:** a Windows code-signing certificate is a separate decision; without one,
  SmartScreen warns on first install.

## 11. Risks

- **I/O contention** slows other VMs during builds. Mitigation: disk bandwidth limit, low
  cpuunits, one build at a time.
- **Public exposure** of release paths. Mitigation: path-limited routes, private repos +
  read-only token, Forgejo kept updated.
- **Keystore loss** means no updates. Mitigation: two offline copies, written down where.
- **Phone battery optimisation** delays updates. Mitigation: Obtainium foreground service, or
  Shizuku.
- **The one-time reinstall per app** loses local data. Mitigation: export first (SSH Sync
  vault), re-scan the voice QR.
- **Single host:** if the R630 is down, there are no updates (apps keep working).

## 12. What I need from you

- Approve or adjust: self-hosted vs the GitHub route; VM 114 sizing; names `apps.stoneyshome.com`
  and `forge.lan.stoneyshome.com`; path-limited public access.
- Approve the Proxmox read (pending link) so I can find the I/O source.
- Monday:
  - install Ubuntu on the new VM;
  - create the Forgejo admin password;
  - generate the keystore and enter the secrets;
  - create the Cloudflare tunnel;
  - decide whether to reuse an existing app key or start fresh;
  - the list of apps (with package ids) to onboard first;
  - phone steps (Obtainium, reinstall).
