# NetStack Doctor

A cross-platform (macOS + Windows + Linux) network diagnostic tool that tests **every layer of
the OSI model** to troubleshoot connection and behavior issues. It is a **fully
standalone desktop app** — the UI renders in its own native window (WKWebView on macOS,
WebView2 on Windows, WebKitGTK on Linux), with everything embedded in the bundle. **No browser, no separate
server, no runtime to install.**

![card UI](docs/screenshot.png)

## What it does

Launch the app and it opens a modern, card-based dashboard in its **own native window**.
Internally it serves the UI over a loopback-only (`127.0.0.1`) connection on an
OS-assigned ephemeral port that only its own window talks to — nothing is exposed and no
web browser is involved. Press **Run diagnostics** and it probes all seven OSI layers,
multiple ways each, reporting every result as **green / yellow / red**. Each layer's card
fills in **the moment that layer finishes** rather than waiting for the slowest one, and
the same button cancels a run in flight. Click any test to open the full raw log of
exactly what was executed (ping transcripts, TLS certificate details, traceroute hops,
etc.).

Every probe captures **maximally verbose output**: each log includes wall-clock +
elapsed timestamps for every step, the exact command/syscall invoked, the complete raw
tool output (boxed), exit status, and the parsed metrics — so you can see precisely what
the tool did and why it reached its verdict.

## AI analysis (local LLM)

A built-in **local LLM analysis** bar interprets the results for you, entirely on your
own hardware — nothing leaves your machine. Two providers are supported:

- **Ollama** (default, `127.0.0.1:11434`)
- **OpenAI-compatible** (`127.0.0.1:1234`) — covers LM Studio, llama.cpp's server,
  vLLM, LocalAI, and anything else exposing `/v1/chat/completions`

1. Pick the provider and enter its address (any host on your network works).
2. Click **Detect models** to refresh the model dropdown with everything installed.
3. Pick a model, run diagnostics, then click **Analyze results with AI**. The full
   layer results (including the verbose logs) are sent to the model, which **streams its
   answer back token by token**, returning a Markdown report: *Summary · Likely Issues ·
   Recommended Actions*, reasoning bottom-up through the stack.

Requests are proxied through the local binary (so the browser never hits CORS), and the
host accepts `ip`, `ip:port`, or a full `http://host:port` URL.

## Reports and history

- **Export** any run as **Markdown** or **JSON** — config, a per-layer summary table,
  every verbose log, and the AI analysis if one was run.
- The last ten runs are kept locally, so you can **compare the current run against an
  earlier one** and see exactly which tests changed status. Regressions are listed first.

| Layer | Probes |
|-------|--------|
| **1 — Physical** | Active interface enumeration, link state, MTU sanity |
| **2 — Data Link** | Default gateway discovery, gateway ARP/L2 resolution, MAC addressing |
| **3 — Network** | IP assignment (v4/v6), ping gateway, ping public v4/v6, traceroute path (v4 **and** v6), public IP + NAT detection |
| **4 — Transport** | TCP/443 + TCP/53 handshakes, UDP/53 datagram round-trip, local socket bind |
| **5 — Session** | TLS session establishment, session resumption, HTTP keep-alive |
| **6 — Presentation** | TLS version & cipher negotiation, certificate chain validation, content encoding |
| **7 — Application** | Captive portal detection, DNS A + AAAA resolution, reverse DNS (PTR), HTTPS request, HTTP/80 availability |

- **DNS resolution** is covered explicitly at the transport (UDP/53, TCP/53), and
  application layers (A, AAAA, PTR).
- **IPv4 and IPv6** are independently toggleable. When IPv6 is enabled but the host has
  no global IPv6 address, the relevant probes are marked *skipped* rather than failed.
- The **target host** and **DNS resolver** are configurable in the toolbar.

## Platform conventions

The UI adapts to the host OS: San Francisco font + rounded cards on macOS, Segoe UI +
Fluent accent + squarer corners on Windows, and it respects system light/dark mode.

## Running

- **macOS (Apple Silicon):** open the **`.dmg`**, then right-click
  **“Install — Bypass Gatekeeper.command”** and choose **Open**. It copies the app to
  Applications, clears the quarantine flag, and launches it. The window opens directly —
  no browser (WKWebView is part of macOS).
- **Windows (x64):** run the **`-setup.exe`** installer. It installs the app and adds a
  **Start Menu** entry (plus an optional desktop icon), and can be installed per-user
  without admin rights. A portable `.zip` is published alongside it if you would rather
  just unzip and run **NetStack Doctor.exe**. It renders in a native WebView2 window
  (WebView2 ships with Windows 10/11).
- **Linux (x64):** install the **`.deb`** (`sudo apt install ./netstack-doctor_*.deb`),
  which adds an application-menu entry, or unpack the tarball. Needs GTK 3 and
  WebKitGTK, which most desktop installs already have.

> **Why the installer step on macOS?** The app is ad-hoc signed but not notarized by
> Apple, so Gatekeeper marks downloaded copies as “damaged/cannot be opened.” The bundled
> installer removes the quarantine attribute
> (`xattr -dr com.apple.quarantine "/Applications/NetStack Doctor.app"`) so it runs. You
> can also drag the app to Applications and run that command yourself — see
> “READ ME FIRST.txt” in the DMG.

> The probes shell out to the OS-provided `ping`/`traceroute`/`arp`/`route` tools, which
> are part of the operating system — not bundled third-party dependencies.

### Headless mode

A server-only build (no native window, opens a loopback port you point any browser at) is
available for development, CI, or headless servers — `./build.sh headless`. Set `NSD_ADDR`
to pin the port (e.g. `NSD_ADDR=127.0.0.1:8696`).

## Building from source

Requires Go 1.21+ and a C toolchain (the native webview uses cgo).

```
./build.sh            # build the macOS .app bundle (Apple Silicon)
./build.sh dmg        # distributable .dmg (drag-to-Applications + Gatekeeper installer)
./build.sh package    # .dmg + checksums for release
./build.sh headless   # server-only binaries (no cgo, cross-compiles to mac/win/linux)
```

Per-platform GUI builds:

```
bash scripts/make-linux-app.sh                          # Linux .deb + tarball
powershell -File scripts\make-windows-installer.ps1     # Windows installer (needs Inno Setup 6.3+)
```

Because the native webview is cgo, each platform's GUI build must run **on that platform**
— macOS locally, Windows and Linux via the bundled GitHub Actions workflow
(`.github/workflows/release.yml`). The `web/` UI is embedded at
compile time via `//go:embed`, so the app has no external assets.
