# OfficeChat

[![Build & release](https://github.com/Himanshusinh/Chat-application-local/actions/workflows/release.yml/badge.svg)](https://github.com/Himanshusinh/Chat-application-local/actions/workflows/release.yml)

Office chat and fast file/folder sharing for **Windows and macOS**, working in
both directions (Windows ↔ Mac, Mac ↔ Mac, Windows ↔ Windows). It doesn't use
Electron or a central server. Each computer runs one small (~7 MB) program,
and computers talk to each other directly.

## Features

- **A real desktop app.** On Windows it has its own window, taskbar icon and
  a **system tray icon** (Open / Quit), and messages pop up from the tray. On
  Mac it has a Dock icon with an unread badge, a menu bar, and notification
  banners. Closing the window keeps it running, so messages and files still
  arrive. It **starts at login** (you can switch this off in Settings).
- **Chat:** an "Everyone" group plus one-to-one chats, with typing
  indicators, ✓✓ delivered / read ticks, unread badges and saved history.
  A direct message to someone who is offline waits and is delivered when
  they come back.
- **Names update live:** when someone changes their name or picture, every
  computer shows the change immediately, including in old messages.
- **Stickers:** 32 built-in stickers, plus your own. Use **＋ Add sticker**
  to add any image or GIF (animated GIFs stay animated). Click a sticker
  someone sent you to save it.
- **Emoji:** a picker with search. An emoji-only message is shown large, and
  **Send big** sends one in a single click.
- **Files and folders of any size:** use 📎 / 📁, paste a screenshot, or
  **drag and drop anywhere in the window**. Drop onto a person in the list
  to send it straight to them. You get live speed and time left, can cancel,
  see image previews, and have Open / Show in folder buttons. Nothing is
  ever overwritten.
- **Dark and light themes:** use the sun/moon button next to your name to
  switch in one click, or go to Settings › Appearance for Light, Dark or
  Match system. Dark is the default. The desktop window's title bar follows
  the theme on both Windows and Mac. Both themes use only blue, light blue,
  black and white.

## Why it works between Mac and Windows

The usual causes of "works Windows-to-Windows only" are handled directly:

| Problem | What OfficeChat does |
|---|---|
| Windows Firewall blocks inbound connections, especially when the network is marked **Public** | One-click **Settings › Allow through Windows Firewall** (and a `.bat`) adds rules for *all* profiles |
| macOS sends `255.255.255.255` broadcasts out of only one interface | Also broadcasts to every interface's own subnet address, and answers newcomers directly |
| `\` vs `/` paths and names Windows can't store (`a:b?.txt`, `CON`, trailing dots) | Paths travel as `a/b/c` and are rebuilt and cleaned up on the receiving side |
| An office proxy intercepting LAN traffic | Peer connections never use the system proxy |
| macOS "Local Network" privacy setting | The `.app` declares it, so macOS asks once |

## Speed

Files are split into 8 MB chunks, and 6 chunks are sent in parallel over
separate connections. The receiver writes each chunk straight to its place in
the file, so there are no temporary copies. Failed chunks are retried
automatically. On a LAN this fills gigabit Ethernet. Over long distances the
parallel connections hide latency. In a loopback test, a 629 MB folder of 203
files went through at about 575 MB/s.

## Install

Always get the newest version from these links:

- **Windows:** [OfficeChat-Setup.exe](https://github.com/Himanshusinh/Chat-application-local/releases/latest/download/OfficeChat-Setup.exe)
  (or the [portable zip](https://github.com/Himanshusinh/Chat-application-local/releases/latest/download/OfficeChat-Windows-Portable.zip) if you have no admin rights)
- **Mac:** [OfficeChat.dmg](https://github.com/Himanshusinh/Chat-application-local/releases/latest/download/OfficeChat.dmg)

**Windows:** run the Setup. It installs to Program Files and adds Start Menu
and Desktop shortcuts. It also adds an entry in *Settings › Apps* with an
uninstaller, and opens the Windows Firewall for OfficeChat. Until the
installer is code-signed, Windows shows "Windows protected your PC". Click
**More info › Run anyway**.

**Mac:** open the `.dmg` and drag OfficeChat into **Applications**.
Automatic updates only work from the Applications folder. The first time,
right-click the app › **Open**, because the app isn't notarised. Then allow
notifications and "find devices on your local network" when asked.

You only install once. After that, OfficeChat **updates itself**.

## Releases and automatic updates

Every push to `main` (except changes to `.md` files only) starts
[GitHub Actions](.github/workflows/release.yml):

1. It builds the Windows version (Setup, portable zip, update files) and the
   Mac version (universal `.dmg` and update zip).
2. It signs an update manifest (`latest.json`) with the release key.
3. It publishes everything as a GitHub Release named `vX.Y.N`. The version
   is the [`VERSION`](VERSION) file plus the build number, e.g. `1.1.42`.

Installed apps check for a new version when they start and every 4 hours.
You can also check right away in **Settings › Updates › Check for
updates**. A new version downloads in the background and is verified
(signature + checksum). Then:

- if the window is closed (running in the tray or Dock) and no files are
  moving, it installs and restarts quietly;
- otherwise the sidebar shows **"Update X is ready — Restart"**. If you
  don't click it, it installs the next time OfficeChat quits.

Chat history, settings and received files are never touched.

**One-time setup (repository owner):** add the signing key as a secret.
GitHub › repository **Settings › Secrets and variables › Actions › New
repository secret**:

- Name: `UPDATE_SIGNING_KEY`
- Value: the `PRIVATE` line from `~/.officechat-release/signing-key.txt`
  on the Mac that set this up (only the long text after the colon).

**Keep that file backed up and secret.** Only releases signed with it are
accepted, so nobody else can push an update to your computers. If it's ever
lost, run `go run ./tools/release keygen`, put the new public key in
`updater.go` and the new private key in the secret. Every computer then has
to install once by hand again.

To bump the major/minor version, edit `VERSION` (e.g. `1.2`).

### Building locally

`./build.sh` builds everything into `dist/`. It needs
`brew install go makensis` and the Xcode command line tools. Local builds are
versioned `X.Y.0`, so installed copies of them also update to the next
GitHub release. A plain `go run .` is a "dev" build and never updates
itself.

## Long distance (other office, home, another city)

Discovery only works on the same LAN. For anything farther away:

1. **Recommended:** install [Tailscale](https://tailscale.com) (free, Windows
   and Mac) on every computer. Then, in OfficeChat, go to **Settings ›
   Connect** and type one colleague's Tailscale IP (`100.x.y.z`). The
   connection is encrypted and direct, and fast.
2. Or use your company VPN and connect by the computer's VPN IP.
3. Or forward TCP port 45456 on the office router and connect from outside
   with `public-ip:45456`. Only do this together with a strong team key.

You only need to connect to **one** computer. OfficeChat learns about
everyone that computer knows.

## Ports

| Port | Use |
|---|---|
| UDP 45454 | Finding colleagues on the LAN |
| TCP 45456 (or the next free port up to 45475) | Messages and files between computers |
| TCP 45480 on `127.0.0.1` | The local window (cannot be reached from the network) |

## Where things are kept

- Received files: `Downloads/OfficeChat` (can be changed in Settings).
- Settings, history and log:
  - macOS: `~/Library/Application Support/OfficeChat`
  - Windows: `%AppData%\OfficeChat`

## Security notes

- Computers only accept each other when they use the same **team key**.
  Change it from the default `office` to a key of your own.
- Traffic on the LAN is not encrypted. Over the internet, use Tailscale or a
  VPN, which encrypt it.

## Code layout

| File | Contents |
|---|---|
| `main.go` | Startup, settings, single-instance check |
| `peers.go` | LAN discovery, peer table, the "connect by address" pinger |
| `p2p.go` | The server other computers talk to, plus sending messages |
| `transfer.go` | Chunked parallel file receiving, safe paths, progress |
| `api.go` | Local API and live events for the window |
| `native_darwin.*`, `native_windows.go` | The desktop window: Cocoa/WebKit on Mac, WebView2 + tray on Windows |
| `stickers.go` | Sticker storage and sending |
| `updater.go`, `update_*.go` | Automatic updates (check, verify, install, restart) |
| `tools/release`, `.github/workflows/release.yml` | Release signing and the automatic build |
| `scripts/build-*.sh` | Windows / Mac build scripts (used locally and by GitHub) |
| `autostart_*.go`, `open_*.go`, `firewall_*.go` | Other Mac/Windows specifics |
| `scripts/installer.nsi` | Windows installer |
| `web/` | The interface (plain HTML/CSS/JS, embedded into the binary) |

To develop, run `go run .`. On a Mac this needs cgo for the native window,
which is on by default. To test two copies on one machine, run
`OFFICECHAT_DATA=/tmp/oc2 go run . -nowindow`. The second copy can't use
LAN discovery, so connect the two with Settings › Connect › `127.0.0.1:45456`.
