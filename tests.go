package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Status values rendered by the UI.
const (
	Green  = "green"  // healthy
	Yellow = "yellow" // degraded / partial / warning
	Red    = "red"    // failed
	Gray   = "gray"   // skipped / not applicable
)

// TestResult is a single probe outcome.
type TestResult struct {
	Name       string   `json:"name"`
	Status     string   `json:"status"`
	Summary    string   `json:"summary"`
	DurationMs int64    `json:"durationMs"`
	Logs       []string `json:"logs"`
}

// LayerResult groups all probes for one OSI layer.
type LayerResult struct {
	Layer   int          `json:"layer"`
	Name    string       `json:"name"`
	Purpose string       `json:"purpose"`
	Status  string       `json:"status"`
	Tests   []TestResult `json:"tests"`
}

type logger struct {
	lines []string
	start time.Time
}

// add appends a raw, unprefixed line.
func (l *logger) add(format string, a ...any) { l.lines = append(l.lines, fmt.Sprintf(format, a...)) }

// step appends a wall-clock + elapsed timestamped line for high-verbosity tracing.
func (l *logger) step(format string, a ...any) {
	now := time.Now()
	prefix := fmt.Sprintf("[%s | t+%5dms] ", now.Format("15:04:05.000"), time.Since(l.start).Milliseconds())
	l.lines = append(l.lines, prefix+fmt.Sprintf(format, a...))
}

// block appends a labelled multi-line block (e.g. raw command output).
func (l *logger) block(label, body string) {
	l.add("┌─ %s", label)
	if strings.TrimSpace(body) == "" {
		l.add("│ (no output)")
	} else {
		for _, ln := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
			l.add("│ %s", ln)
		}
	}
	l.add("└─")
}

func timed(name string, fn func(l *logger) (string, string)) TestResult {
	start := time.Now()
	l := &logger{start: start}
	l.step("probe %q started on %s/%s", name, runtime.GOOS, runtime.GOARCH)
	status, summary := fn(l)
	l.step("probe finished: status=%s summary=%q", status, summary)
	return TestResult{
		Name:       name,
		Status:     status,
		Summary:    summary,
		DurationMs: time.Since(start).Milliseconds(),
		Logs:       l.lines,
	}
}

// rollup chooses the worst meaningful status of the child tests.
func rollup(tests []TestResult) string {
	worst := Gray
	rank := map[string]int{Gray: 0, Green: 1, Yellow: 2, Red: 3}
	for _, t := range tests {
		if rank[t.Status] > rank[worst] {
			worst = t.Status
		}
	}
	if worst == Gray {
		return Gray
	}
	return worst
}

// RunAllLayers executes the 7 OSI layer suites. Layers run in parallel; each
// layer's probes run sequentially so logs stay readable.
func RunAllLayers(cfg RunConfig) []LayerResult {
	type job struct {
		idx int
		fn  func(RunConfig) LayerResult
	}
	jobs := []job{
		{0, layer1Physical},
		{1, layer2DataLink},
		{2, layer3Network},
		{3, layer4Transport},
		{4, layer5Session},
		{5, layer6Presentation},
		{6, layer7Application},
	}
	out := make([]LayerResult, len(jobs))
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			out[j.idx] = j.fn(cfg)
		}(j)
	}
	wg.Wait()
	return out
}

// ---------------- Layer 1: Physical ----------------

func layer1Physical(cfg RunConfig) LayerResult {
	var tests []TestResult

	tests = append(tests, timed("Active network interfaces", func(l *logger) (string, string) {
		l.step("enumerating link-layer interfaces via net.Interfaces() syscall")
		ifaces := interfaceSummary()
		l.step("kernel reported %d total interface(s)", len(ifaces))
		active := 0
		for _, ifc := range ifaces {
			flag := "down"
			if ifc.Up {
				flag = "UP"
			}
			kind := "ethernet/physical"
			if ifc.Loop {
				kind = "loopback"
			}
			l.add("")
			l.add("interface: %s", ifc.Name)
			l.add("  state    : %s", flag)
			l.add("  type     : %s", kind)
			l.add("  hw addr  : %s", emptyDash(ifc.MAC))
			l.add("  mtu      : %d bytes", ifc.MTU)
			if len(ifc.Addrs) == 0 {
				l.add("  addresses: (none assigned)")
			}
			for _, a := range ifc.Addrs {
				l.add("  address  : %s", a)
			}
			if ifc.Up && !ifc.Loop && ifc.MAC != "" {
				active++
			}
		}
		l.step("classified %d interface(s) as active physical links", active)
		if active == 0 {
			return Red, "No active physical interface detected"
		}
		return Green, fmt.Sprintf("%d active physical interface(s)", active)
	}))

	tests = append(tests, timed("Link MTU sanity", func(l *logger) (string, string) {
		l.step("inspecting MTU of each active link (1280=IPv6 min, 1500=ethernet std, >1500=jumbo)")
		ifaces := interfaceSummary()
		low := 0
		ok := 0
		for _, ifc := range ifaces {
			if !ifc.Up || ifc.Loop {
				continue
			}
			note := "standard"
			switch {
			case ifc.MTU < 1280:
				note = "BELOW IPv6 minimum (1280)"
			case ifc.MTU > 1500:
				note = "jumbo frame"
			}
			l.add("%-14s mtu=%-6d %s", ifc.Name, ifc.MTU, note)
			if ifc.MTU < 1280 {
				low++
			} else {
				ok++
			}
		}
		if ok == 0 {
			return Red, "No usable links"
		}
		if low > 0 {
			return Yellow, fmt.Sprintf("%d link(s) below 1280 MTU", low)
		}
		return Green, "All active links have a healthy MTU"
	}))

	return LayerResult{1, "Physical", "Hardware links, interfaces, MTU", rollup(tests), tests}
}

// ---------------- Layer 2: Data Link ----------------

func layer2DataLink(cfg RunConfig) LayerResult {
	var tests []TestResult

	gw, gwErr := defaultGateway()

	tests = append(tests, timed("Default gateway discovery", func(l *logger) (string, string) {
		l.step("querying OS routing table for default route (0.0.0.0/0)")
		l.add("method: %s", gatewayMethod())
		if gwErr != nil {
			l.add("error: %v", gwErr)
			return Red, "Could not determine default gateway"
		}
		l.step("default gateway resolved to %s", gw)
		l.add("default gateway = %s", gw)
		return Green, "Default gateway: "+gw
	}))

	tests = append(tests, timed("Gateway ARP / L2 reachability", func(l *logger) (string, string) {
		if gwErr != nil {
			l.step("no gateway available, skipping")
			return Gray, "No gateway to resolve"
		}
		// Prime the ARP cache with a ping first.
		l.step("priming neighbor cache: %s", "ping "+gw)
		pr := ping(gw, false, 2)
		l.add("exec: %s", pr.Cmd)
		l.block("ping output", pr.Raw)
		l.add("result: reachable=%v loss=%s avg=%.2fms (%s)", pr.OK, emptyDash(pr.Loss), pr.AvgMs, pr.ExitInfo)
		l.step("resolving L2 hardware address from ARP/neighbor table")
		l.add("exec: arp -n %s  (fallback: arp -a %s)", gw, gw)
		mac, err := arpLookup(gw)
		if err != nil {
			l.add("arp lookup failed: %v", err)
			if pr.OK {
				return Yellow, "Gateway reachable but MAC not in ARP table"
			}
			return Red, "Gateway MAC could not be resolved"
		}
		l.step("gateway L2 (MAC) address = %s", mac)
		l.add("gateway MAC = %s", mac)
		l.add("OUI (vendor prefix) = %s", ouiPrefix(mac))
		return Green, "Gateway L2 address resolved: "+mac
	}))

	tests = append(tests, timed("MAC addressing present", func(l *logger) (string, string) {
		ifaces := interfaceSummary()
		withMAC := 0
		for _, ifc := range ifaces {
			if ifc.Up && !ifc.Loop && ifc.MAC != "" {
				withMAC++
				l.add("%s -> %s", ifc.Name, ifc.MAC)
			}
		}
		if withMAC == 0 {
			return Red, "No hardware (MAC) addresses found"
		}
		return Green, fmt.Sprintf("%d interface(s) with hardware addresses", withMAC)
	}))

	return LayerResult{2, "Data Link", "MAC addressing, ARP, gateway L2", rollup(tests), tests}
}

// ---------------- Layer 3: Network ----------------

func layer3Network(cfg RunConfig) LayerResult {
	var tests []TestResult
	v4, v6 := localAddrs()

	tests = append(tests, timed("IP address assignment", func(l *logger) (string, string) {
		l.step("collecting routable unicast addresses from all non-loopback links")
		l.add("IPv4 addresses (%d):", len(v4))
		for _, a := range v4 {
			l.add("  %s", a)
		}
		l.add("IPv6 addresses (%d):", len(v6))
		for _, a := range v6 {
			l.add("  %s", a)
		}
		l.step("config requested: ipv4=%v ipv6=%v", cfg.IPv4, cfg.IPv6)
		switch {
		case cfg.IPv4 && len(v4) == 0:
			if cfg.IPv6 && len(v6) > 0 {
				return Yellow, "No IPv4 address (IPv6 only)"
			}
			return Red, "No routable IP address assigned"
		case len(v4) == 0 && len(v6) == 0:
			return Red, "No routable IP address assigned"
		default:
			return Green, fmt.Sprintf("%d IPv4 / %d IPv6 address(es)", len(v4), len(v6))
		}
	}))

	// Gateway ping
	if gw, err := defaultGateway(); err == nil {
		tests = append(tests, timed("Ping default gateway", func(l *logger) (string, string) {
			l.step("ICMP echo to default gateway %s (3 packets)", gw)
			pr := ping(gw, false, 3)
			l.add("exec: %s", pr.Cmd)
			l.block("raw ping output", pr.Raw)
			l.add("exit: %s", pr.ExitInfo)
			l.add("parsed: reachable=%v loss=%s avg=%.2fms", pr.OK, emptyDash(pr.Loss), pr.AvgMs)
			if pr.OK {
				return Green, fmt.Sprintf("Gateway %s reachable (%.0f ms)", gw, pr.AvgMs)
			}
			return Red, "Gateway unreachable"
		}))
	}

	// Public reachability per family
	addFamilyPing := func(label, host string, v6 bool) {
		tests = append(tests, timed("Ping "+label, func(l *logger) (string, string) {
			fam := "IPv4"
			if v6 {
				fam = "IPv6"
			}
			l.step("ICMP echo to public %s anchor %s (3 packets)", fam, host)
			pr := ping(host, v6, 3)
			l.add("exec: %s", pr.Cmd)
			l.block("raw ping output", pr.Raw)
			l.add("exit: %s", pr.ExitInfo)
			l.add("parsed: reachable=%v loss=%s avg=%.2fms", pr.OK, emptyDash(pr.Loss), pr.AvgMs)
			if pr.Err != "" {
				l.add("note: ICMP may be administratively filtered upstream")
			}
			if pr.OK {
				return Green, fmt.Sprintf("%s reachable (%.0f ms)", host, pr.AvgMs)
			}
			return Red, host+" unreachable (ICMP may be filtered)"
		}))
	}
	if cfg.IPv4 {
		addFamilyPing("public IPv4 (1.1.1.1)", "1.1.1.1", false)
	}
	if cfg.IPv6 {
		if hasGlobalIPv6() {
			addFamilyPing("public IPv6 (2606:4700:4700::1111)", "2606:4700:4700::1111", true)
		} else {
			tests = append(tests, skipped("Ping public IPv6", "No global IPv6 address on this host"))
		}
	}

	// Traceroute path
	tests = append(tests, timed("Path / traceroute to "+cfg.Target, func(l *logger) (string, string) {
		l.step("tracing network path to %s (max 20 hops)", cfg.Target)
		raw, hops, cmd := traceroute(cfg.Target, false, 20)
		l.add("exec: %s", cmd)
		l.block("raw traceroute output", raw)
		l.step("counted %d responding hop(s)", hops)
		if hops == 0 {
			return Yellow, "No traceroute hops returned"
		}
		return Green, fmt.Sprintf("%d hop(s) along the path", hops)
	}))

	return LayerResult{3, "Network", "IP routing, gateway, ICMP, path", rollup(tests), tests}
}

// ---------------- Layer 4: Transport ----------------

func layer4Transport(cfg RunConfig) LayerResult {
	var tests []TestResult

	tcpProbe := func(label, host string, port int, v6 bool) {
		tests = append(tests, timed(label, func(l *logger) (string, string) {
			network := "tcp4"
			if v6 {
				network = "tcp6"
			}
			addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
			l.step("resolving %s for family %s", host, network)
			if ips, rerr := net.LookupHost(host); rerr == nil {
				for _, ip := range ips {
					l.add("  candidate %s", ip)
				}
			} else {
				l.add("  (host already literal or resolution deferred to dialer: %v)", rerr)
			}
			l.step("opening TCP socket to %s (SYN -> SYN/ACK -> ACK), 4s timeout", addr)
			start := time.Now()
			d := net.Dialer{Timeout: 4 * time.Second}
			conn, err := d.Dial(network, addr)
			if err != nil {
				l.add("dial %s %s FAILED after %s", network, addr, time.Since(start).Round(time.Millisecond))
				l.add("error: %v", err)
				return Red, fmt.Sprintf("TCP %d unreachable", port)
			}
			defer conn.Close()
			rtt := time.Since(start)
			l.step("three-way handshake completed")
			l.add("local  endpoint : %s", conn.LocalAddr())
			l.add("remote endpoint : %s", conn.RemoteAddr())
			l.add("handshake RTT   : %s", rtt.Round(time.Millisecond))
			status := Green
			verdict := "within normal range"
			if rtt > 800*time.Millisecond {
				status = Yellow
				verdict = "elevated latency (>800ms)"
			}
			l.add("assessment      : %s", verdict)
			return status, fmt.Sprintf("TCP handshake ok (%s)", rtt.Round(time.Millisecond))
		}))
	}

	if cfg.IPv4 {
		tcpProbe("TCP/443 HTTPS handshake (IPv4)", cfg.Target, 443, false)
		tcpProbe("TCP/53 DNS over IPv4 ("+cfg.DNS+")", cfg.DNS, 53, false)
	}
	if cfg.IPv6 {
		if hasGlobalIPv6() {
			tcpProbe("TCP/443 HTTPS handshake (IPv6)", cfg.Target, 443, true)
		} else {
			tests = append(tests, skipped("TCP/443 HTTPS handshake (IPv6)", "No global IPv6 address"))
		}
	}

	// UDP DNS round-trip (connectionless transport)
	tests = append(tests, timed("UDP/53 round-trip ("+cfg.DNS+")", func(l *logger) (string, string) {
		l.step("building raw DNS/UDP query packet (type A) for %q", cfg.Target)
		l.add("dns server  : %s:53 (UDP, connectionless)", cfg.DNS)
		l.add("query packet: %s", hexPreview(buildDNSQuery(cfg.Target)))
		l.step("sending datagram and awaiting response (3s deadline)")
		ms, err := udpDNSRoundTrip(cfg.DNS, cfg.Target)
		if err != nil {
			l.add("udp dns error: %v", err)
			return Red, "UDP DNS query failed"
		}
		l.add("response received, round-trip = %d ms", ms)
		return Green, fmt.Sprintf("UDP datagram round-trip ok (%d ms)", ms)
	}))

	// Ephemeral port / local stack check
	tests = append(tests, timed("Local TCP stack (ephemeral bind)", func(l *logger) (string, string) {
		l.step("requesting kernel to bind an ephemeral TCP port on 127.0.0.1")
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			l.add("listen error: %v", err)
			return Red, "Cannot open local TCP socket"
		}
		defer ln.Close()
		l.add("kernel assigned ephemeral socket: %s", ln.Addr())
		l.add("local transport stack can create/bind/listen sockets")
		return Green, "Local transport stack operational"
	}))

	return LayerResult{4, "Transport", "TCP/UDP ports, handshakes, sockets", rollup(tests), tests}
}

func udpDNSRoundTrip(server, name string) (int64, error) {
	addr := net.JoinHostPort(server, "53")
	conn, err := net.DialTimeout("udp", addr, 3*time.Second)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	query := buildDNSQuery(name)
	start := time.Now()
	if _, err := conn.Write(query); err != nil {
		return 0, err
	}
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		return 0, err
	}
	if n < 12 {
		return 0, fmt.Errorf("short DNS response")
	}
	return time.Since(start).Milliseconds(), nil
}

// buildDNSQuery builds a minimal A-record query packet.
func buildDNSQuery(name string) []byte {
	msg := []byte{0x12, 0x34, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	for _, label := range strings.Split(name, ".") {
		msg = append(msg, byte(len(label)))
		msg = append(msg, []byte(label)...)
	}
	msg = append(msg, 0x00)             // root
	msg = append(msg, 0x00, 0x01)       // type A
	msg = append(msg, 0x00, 0x01)       // class IN
	return msg
}

// ---------------- Layer 5: Session ----------------

func layer5Session(cfg RunConfig) LayerResult {
	var tests []TestResult

	tests = append(tests, timed("TLS session establishment", func(l *logger) (string, string) {
		l.step("establishing a stateful TLS session with %s:443", cfg.Target)
		state, err := tlsHandshake(cfg.Target, false, l)
		if err != nil {
			return Red, "Could not establish session"
		}
		l.add("session established: version=%s cipher=%s", tlsVersionName(state.Version), tls.CipherSuiteName(state.CipherSuite))
		l.add("ALPN protocol     : %s", emptyDash(state.NegotiatedProtocol))
		l.add("OCSP stapled      : %v", len(state.OCSPResponse) > 0)
		return Green, "Session established with "+cfg.Target
	}))

	tests = append(tests, timed("TLS session resumption", func(l *logger) (string, string) {
		cache := tls.NewLRUClientSessionCache(4)
		dial := func(n int) (bool, error) {
			t0 := time.Now()
			d := net.Dialer{Timeout: 5 * time.Second}
			raw, err := d.Dial("tcp", net.JoinHostPort(cfg.Target, "443"))
			if err != nil {
				return false, err
			}
			defer raw.Close()
			c := tls.Client(raw, &tls.Config{ServerName: cfg.Target, ClientSessionCache: cache})
			if err := c.Handshake(); err != nil {
				return false, err
			}
			defer c.Close()
			st := c.ConnectionState()
			l.add("handshake #%d: %s in %s, resumed=%v", n, tlsVersionName(st.Version),
				time.Since(t0).Round(time.Millisecond), st.DidResume)
			return st.DidResume, nil
		}
		l.step("handshake #1 — populating client session cache")
		if _, err := dial(1); err != nil {
			l.add("first handshake failed: %v", err)
			return Red, "Session could not be initiated"
		}
		l.step("handshake #2 — attempting to resume cached session ticket")
		resumed, err := dial(2)
		if err != nil {
			l.add("second handshake failed: %v", err)
			return Yellow, "Re-handshake failed"
		}
		if resumed {
			return Green, "Server supports fast session resumption"
		}
		return Yellow, "Sessions work but server did not resume (full handshake)"
	}))

	tests = append(tests, timed("HTTP keep-alive (persistent session)", func(l *logger) (string, string) {
		l.step("issuing 2 sequential HTTPS requests over one keep-alive connection")
		client := &http.Client{Timeout: 8 * time.Second}
		url := "https://" + cfg.Target + "/"
		for i := 1; i <= 2; i++ {
			t0 := time.Now()
			resp, err := client.Get(url)
			if err != nil {
				l.add("request %d failed: %v", i, err)
				return Red, "Persistent session failed"
			}
			n, _ := io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			l.add("request %d: %s proto=%s bytes=%d in %s", i, resp.Status, resp.Proto, n, time.Since(t0).Round(time.Millisecond))
		}
		l.add("connection reused without re-handshake (socket kept alive)")
		return Green, "Persistent HTTP session maintained"
	}))

	return LayerResult{5, "Session", "Session setup, resumption, keep-alive", rollup(tests), tests}
}

func tlsHandshake(host string, v6 bool, l *logger) (tls.ConnectionState, error) {
	network := "tcp"
	if v6 {
		network = "tcp6"
	}
	d := net.Dialer{Timeout: 6 * time.Second}
	l.step("dialing TCP %s for TLS", net.JoinHostPort(host, "443"))
	raw, err := d.Dial(network, net.JoinHostPort(host, "443"))
	if err != nil {
		l.add("dial error: %v", err)
		return tls.ConnectionState{}, err
	}
	defer raw.Close()
	l.add("TCP connected: %s -> %s", raw.LocalAddr(), raw.RemoteAddr())
	c := tls.Client(raw, &tls.Config{ServerName: host})
	t0 := time.Now()
	l.step("starting TLS handshake (SNI=%s)", host)
	if err := c.Handshake(); err != nil {
		l.add("handshake error: %v", err)
		return tls.ConnectionState{}, err
	}
	defer c.Close()
	st := c.ConnectionState()
	l.add("handshake ok in %s", time.Since(t0).Round(time.Millisecond))
	for i, cert := range st.PeerCertificates {
		role := "leaf"
		if i > 0 {
			role = "intermediate/root"
		}
		l.add("  cert[%d] (%s): CN=%q issuer=%q", i, role, cert.Subject.CommonName, cert.Issuer.CommonName)
		if i == 0 && len(cert.DNSNames) > 0 {
			l.add("           SANs: %s", strings.Join(cert.DNSNames, ", "))
		}
	}
	return st, nil
}

// ---------------- Layer 6: Presentation ----------------

func layer6Presentation(cfg RunConfig) LayerResult {
	var tests []TestResult

	tests = append(tests, timed("TLS version & cipher negotiation", func(l *logger) (string, string) {
		st, err := tlsHandshake(cfg.Target, false, l)
		if err != nil {
			return Red, "TLS negotiation failed"
		}
		l.add("version = %s", tlsVersionName(st.Version))
		l.add("cipher  = %s", tls.CipherSuiteName(st.CipherSuite))
		l.add("ALPN    = %s", emptyDash(st.NegotiatedProtocol))
		if st.Version < tls.VersionTLS12 {
			return Yellow, "Negotiated outdated TLS ("+tlsVersionName(st.Version)+")"
		}
		return Green, fmt.Sprintf("%s / %s", tlsVersionName(st.Version), tls.CipherSuiteName(st.CipherSuite))
	}))

	tests = append(tests, timed("Certificate chain validation", func(l *logger) (string, string) {
		st, err := tlsHandshake(cfg.Target, false, l)
		if err != nil {
			return Red, "Could not retrieve certificate"
		}
		if len(st.PeerCertificates) == 0 {
			return Red, "No certificate presented"
		}
		leaf := st.PeerCertificates[0]
		l.step("inspecting leaf certificate and validating chain against system trust store")
		l.add("subject      = %s", leaf.Subject.String())
		l.add("issuer       = %s", leaf.Issuer.String())
		l.add("serial       = %s", leaf.SerialNumber.String())
		l.add("sig algorithm= %s", leaf.SignatureAlgorithm.String())
		l.add("public key   = %s", leaf.PublicKeyAlgorithm.String())
		l.add("SANs         = %s", strings.Join(leaf.DNSNames, ", "))
		l.add("not before   = %s", leaf.NotBefore.Format(time.RFC1123))
		l.add("not after    = %s", leaf.NotAfter.Format(time.RFC1123))
		l.add("chain depth  = %d certificate(s)", len(st.PeerCertificates))
		// Verify against system roots.
		inter := x509.NewCertPool()
		for _, c := range st.PeerCertificates[1:] {
			inter.AddCert(c)
		}
		chains, verr := leaf.Verify(x509.VerifyOptions{DNSName: cfg.Target, Intermediates: inter})
		if verr != nil || len(chains) == 0 {
			if verr != nil {
				l.add("verify error: %v", verr)
			}
			l.add("WARNING: chain did not validate against system roots")
			return Yellow, "Certificate presented but chain not fully trusted"
		}
		days := time.Until(leaf.NotAfter).Hours() / 24
		l.add("days until expiry = %.0f", days)
		if days < 0 {
			return Red, "Certificate expired"
		}
		if days < 14 {
			return Yellow, fmt.Sprintf("Certificate valid but expires in %.0f days", days)
		}
		return Green, fmt.Sprintf("Trusted certificate, %.0f days remaining", days)
	}))

	tests = append(tests, timed("Content compression support", func(l *logger) (string, string) {
		req, _ := http.NewRequest("GET", "https://"+cfg.Target+"/", nil)
		req.Header.Set("Accept-Encoding", "gzip, br")
		client := &http.Client{Timeout: 8 * time.Second,
			Transport: &http.Transport{DisableCompression: true}}
		resp, err := client.Do(req)
		if err != nil {
			l.add("request error: %v", err)
			return Yellow, "Could not assess compression"
		}
		defer resp.Body.Close()
		enc := resp.Header.Get("Content-Encoding")
		l.add("Content-Encoding = %s", emptyDash(enc))
		l.add("Content-Type     = %s", resp.Header.Get("Content-Type"))
		if enc == "" {
			return Yellow, "Server returned uncompressed payload"
		}
		return Green, "Presentation-layer encoding negotiated ("+enc+")"
	}))

	return LayerResult{6, "Presentation", "TLS, certificates, encoding/compression", rollup(tests), tests}
}

// ---------------- Layer 7: Application ----------------

func layer7Application(cfg RunConfig) LayerResult {
	var tests []TestResult

	// DNS resolution — A records
	tests = append(tests, timed("DNS resolution — A (IPv4)", func(l *logger) (string, string) {
		l.step("resolving A records for %q via system resolver", cfg.Target)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if cn, e := net.DefaultResolver.LookupCNAME(ctx, cfg.Target); e == nil {
			l.add("canonical name (CNAME): %s", cn)
		}
		t0 := time.Now()
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", cfg.Target)
		l.add("lookup latency: %s", time.Since(t0).Round(time.Millisecond))
		if err != nil {
			l.add("lookup error: %v", err)
			return Red, "A-record resolution failed"
		}
		for _, ip := range ips {
			l.add("A %s", ip.String())
		}
		if len(ips) == 0 {
			return Red, "No A records returned"
		}
		return Green, fmt.Sprintf("Resolved %d IPv4 address(es)", len(ips))
	}))

	// DNS resolution — AAAA records
	if cfg.IPv6 {
		tests = append(tests, timed("DNS resolution — AAAA (IPv6)", func(l *logger) (string, string) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip6", cfg.Target)
			if err != nil || len(ips) == 0 {
				if err != nil {
					l.add("lookup error: %v", err)
				}
				return Yellow, "No AAAA (IPv6) records for "+cfg.Target
			}
			for _, ip := range ips {
				l.add("AAAA %s", ip.String())
			}
			return Green, fmt.Sprintf("Resolved %d IPv6 address(es)", len(ips))
		}))
	}

	// Reverse DNS / PTR for the configured resolver
	tests = append(tests, timed("Reverse DNS (PTR) of resolver", func(l *logger) (string, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		names, err := net.DefaultResolver.LookupAddr(ctx, cfg.DNS)
		if err != nil || len(names) == 0 {
			l.add("ptr error/empty: %v", err)
			return Yellow, "No PTR record for "+cfg.DNS
		}
		for _, n := range names {
			l.add("PTR %s", n)
		}
		return Green, "Reverse DNS resolves: "+strings.TrimSuffix(names[0], ".")
	}))

	// HTTPS application request
	tests = append(tests, timed("HTTPS application request", func(l *logger) (string, string) {
		client := &http.Client{Timeout: 10 * time.Second}
		start := time.Now()
		l.step("GET https://%s/ (10s timeout, following redirects)", cfg.Target)
		resp, err := client.Get("https://" + cfg.Target + "/")
		if err != nil {
			l.add("GET error: %v", err)
			return Red, "HTTPS request failed"
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		l.add("status   = %s", resp.Status)
		l.add("proto    = %s", resp.Proto)
		l.add("latency  = %s", time.Since(start).Round(time.Millisecond))
		l.add("bytes    = %d (first chunk of body)", len(body))
		l.block("response headers", headerDump(resp.Header))
		if resp.StatusCode >= 500 {
			return Red, fmt.Sprintf("Server error %s", resp.Status)
		}
		if resp.StatusCode >= 400 {
			return Yellow, fmt.Sprintf("Client error %s", resp.Status)
		}
		return Green, fmt.Sprintf("%s via %s", resp.Status, resp.Proto)
	}))

	// Plain HTTP (port 80) redirect/availability
	tests = append(tests, timed("HTTP/80 availability", func(l *logger) (string, string) {
		client := &http.Client{
			Timeout: 8 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		resp, err := client.Get("http://" + cfg.Target + "/")
		if err != nil {
			l.add("GET error: %v", err)
			return Yellow, "Port 80 not reachable (may be HTTPS-only)"
		}
		defer resp.Body.Close()
		loc := resp.Header.Get("Location")
		l.add("status = %s", resp.Status)
		if loc != "" {
			l.add("redirect -> %s", loc)
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return Green, "HTTP/80 redirects to secure endpoint"
		}
		return Green, "HTTP/80 responded: "+resp.Status
	}))

	return LayerResult{7, "Application", "DNS, HTTP/HTTPS, app protocols", rollup(tests), tests}
}

// ---------------- shared helpers ----------------

func ouiPrefix(mac string) string {
	parts := strings.Split(mac, ":")
	if len(parts) >= 3 {
		return strings.ToUpper(parts[0] + ":" + parts[1] + ":" + parts[2])
	}
	return "—"
}

func hexPreview(b []byte) string {
	var sb strings.Builder
	for i, x := range b {
		if i >= 32 {
			sb.WriteString("…")
			break
		}
		fmt.Fprintf(&sb, "%02x ", x)
	}
	return strings.TrimSpace(sb.String())
}

func headerDump(h http.Header) string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&sb, "%s: %s\n", k, strings.Join(h[k], ", "))
	}
	return sb.String()
}

func skipped(name, why string) TestResult {
	return TestResult{Name: name, Status: Gray, Summary: why, Logs: []string{why}}
}

func emptyDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS13:
		return "TLS 1.3"
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS11:
		return "TLS 1.1"
	case tls.VersionTLS10:
		return "TLS 1.0"
	}
	return fmt.Sprintf("0x%04x", v)
}

