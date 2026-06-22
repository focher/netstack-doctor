# NetStack Doctor

A cross-platform (macOS + Windows) network diagnostic tool that tests **every layer of
the OSI model** to troubleshoot connection and behavior issues. It is a **fully
standalone desktop app** — the UI renders in its own native window (WKWebView on macOS,
WebView2 on Windows), with everything embedded in the bundle. **No browser, no separate
server, no runtime to install.**

![card UI](docs/screenshot.png)

## What it does

Launch the app and it opens a modern, card-based dashboard in its **own native window**.
Internally it serves the UI over a loopback-only (`127.0.0.1`) connection on an
OS-assigned ephemeral port that only its own window talks to — nothing is exposed and no
web browser is involved. Press **Run diagnostics** and it probes all seven OSI layers,
multiple ways each, reporting every result as **green / yellow / red**. Click any test to
open the full raw log of exactly what was executed (ping transcripts, TLS certificate
details, traceroute hops, etc.).

Every probe captures **maximally verbose output**: each log includes wall-clock +
elapsed timestamps for every step, the exact command/syscall invoked, the complete raw
tool output (boxed), exit status, and the parsed metrics — so you can see precisely what
the tool did and why it reached its verdict.

## AI analysis (local LLM)

A built-in **local LLM analysis** bar (default provider: **Ollama**) interprets the
results for you, entirely on your own hardware — nothing leaves your machine.

1. Enter the Ollama address (default `127.0.0.1:11434`; can be any host on your network).
2. Click **Detect models** — the app queries Ollama's `/api/tags` and refreshes the
   model dropdown with everything installed (name, parameter size, on-disk size).
3. Pick a model, run diagnostics, then click **Analyze results with AI**. The full
   layer results (including the verbose logs) are sent to the model, which returns a
   Markdown report: *Summary · Likely Issues · Recommended Actions*, reasoning bottom-up
   through the stack.

Requests are proxied through the local binary (so the browser never hits CORS), and the
host accepts `ip`, `ip:port`, or a full `http://host:port` URL.

| Layer | Probes |
|-------|--------|
| **1 — Physical** | Active interface enumeration, link state, MTU sanity |
| **2 — Data Link** | Default gateway discovery, gateway ARP/L2 resolution, MAC addressing |
| **3 — Network** | IP assignment (v4/v6), ping gateway, ping public v4/v6, traceroute path |
| **4 — Transport** | TCP/443 + TCP/53 handshakes, UDP/53 datagram round-trip, local socket bind |
| **5 — Session** | TLS session establishment, session resumption, HTTP keep-alive |
| **6 — Presentation** | TLS version & cipher negotiation, certificate chain validation, content encoding |
| **7 — Application** | DNS A + AAAA resolution, reverse DNS (PTR), HTTPS request, HTTP/80 availability |

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
- **Windows (x64):** unzip and double-click **NetStack Doctor.exe**. It renders in a
  native WebView2 window (WebView2 ships with Windows 10/11).

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
./build.sh package    # .app + tar.gz + checksums for release
./build.sh headless   # server-only binaries (no cgo, cross-compiles to mac/win/linux)
```

Because the native webview is cgo, each platform's GUI build must run **on that platform**
— macOS locally, Windows via the bundled GitHub Actions workflow
(`.github/workflows/release.yml`) on a Windows runner. The `web/` UI is embedded at
compile time via `//go:embed`, so the app has no external assets.
