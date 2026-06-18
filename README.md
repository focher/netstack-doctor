# NetStack Doctor

A cross-platform (macOS + Windows) network diagnostic tool that tests **every layer of
the OSI model** to troubleshoot connection and behavior issues. It ships as a **single
self-contained binary** — the entire UI is embedded in the executable, so there is
nothing to install and no runtime dependencies.

![card UI](docs/screenshot.png)

## What it does

When launched, the binary starts a tiny local web server (bound to `127.0.0.1` only)
and opens your default browser to a modern, card-based dashboard. Press **Run
diagnostics** and it probes all seven OSI layers, multiple ways each, and reports every
result as **green / yellow / red**. Click any test to open the full raw log of exactly
what was executed (ping transcripts, TLS certificate details, traceroute hops, etc.).

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

Just double-click (or run) the binary for your platform:

```
# macOS (Apple Silicon)
./netstack-doctor-macos-arm64

# macOS (Intel)
./netstack-doctor-macos-amd64

# Windows
netstack-doctor.exe
```

Override the listen address with `NSD_ADDR` (default `127.0.0.1:8696`).

> The probes shell out to the OS-provided `ping`/`traceroute`/`arp`/`route` tools, which
> are part of the operating system — not bundled third-party dependencies.

## Building from source

Requires Go 1.21+ (for `embed`).

```
go build -o netstack-doctor .            # current platform
./build.sh                               # all macOS + Windows targets into ./dist
```

The `web/` directory is embedded at compile time via `//go:embed`, which is why the
output is a single binary with no external assets.
