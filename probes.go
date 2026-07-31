package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Endpoints used by the connectivity probes below. Both are Cloudflare, which
// the app already depends on for its default target and resolver, so these add
// no new third party.
const (
	// Returns a plain-text key=value block including the client's public IP.
	traceHost = "one.one.one.one"
	traceURL  = "https://one.one.one.one/cdn-cgi/trace"
	// Standard captive-portal probe: a bare HTTP 204 with no body.
	portalURL = "http://cp.cloudflare.com/generate_204"
)

// familyClient returns an HTTP client pinned to one address family, so an
// IPv4-only or IPv6-only result can be attributed to that family rather than
// to whichever one the dialer happened to prefer.
func familyClient(network string, timeout time.Duration) (*http.Client, func()) {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			d := net.Dialer{Timeout: timeout}
			return d.DialContext(ctx, network, addr)
		},
		DisableKeepAlives: true,
	}
	return &http.Client{Timeout: timeout, Transport: tr}, tr.CloseIdleConnections
}

// publicIP asks the trace endpoint how this host appears from the internet.
func publicIP(ctx context.Context, network string) (string, error) {
	client, closeIdle := familyClient(network, 8*time.Second)
	defer closeIdle()

	req, err := http.NewRequestWithContext(ctx, "GET", traceURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("trace endpoint returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "ip="); ok {
			if net.ParseIP(v) == nil {
				return "", fmt.Errorf("trace endpoint returned an unparseable address %q", v)
			}
			return v, nil
		}
	}
	return "", fmt.Errorf("no ip= field in trace response")
}

// publicIPProbe reports how the host appears from the internet and whether it
// is behind NAT — the context needed to read the rest of a diagnostic report.
func publicIPProbe(ctx context.Context, cfg RunConfig, l *logger, localV4, localV6 []string) (string, string) {
	local := make(map[string]bool, len(localV4)+len(localV6))
	for _, a := range append(append([]string{}, localV4...), localV6...) {
		local[a] = true
	}

	var found []string
	var natted bool

	probe := func(fam, network string, want bool) {
		if !want {
			return
		}
		l.step("asking %s over %s how this host appears from the internet", traceHost, fam)
		ip, err := publicIP(ctx, network)
		if err != nil {
			l.add("%-5s public address: lookup failed: %v", fam, err)
			return
		}
		found = append(found, ip)
		l.add("%-5s public address: %s", fam, ip)
		if local[ip] {
			l.add("      matches a local interface address — this host is directly addressable")
		} else {
			natted = true
			l.add("      does not match any local address — traffic is being translated (NAT)")
		}
	}

	probe("IPv4", "tcp4", cfg.IPv4)
	// Only ask over v6 if the host actually has a routable v6 address.
	probe("IPv6", "tcp6", cfg.IPv6 && len(localV6) > 0)

	if len(found) == 0 {
		if ctx.Err() != nil {
			return Red, "Could not determine public IP"
		}
		l.add("no public address could be determined; egress to %s may be blocked", traceHost)
		return Yellow, "Public IP could not be determined"
	}
	if natted {
		return Green, "Public IP " + strings.Join(found, ", ") + " (behind NAT)"
	}
	return Green, "Public IP " + strings.Join(found, ", ") + " (no NAT)"
}

// captivePortalProbe detects an intercepting portal — the most common cause of
// "connected but no internet". A portal is invisible to the other probes
// because DNS resolves, TCP connects and HTTP answers 200; what gives it away
// is that a URL contractually defined to return an empty 204 returns something
// else instead.
func captivePortalProbe(ctx context.Context, l *logger) (string, string) {
	// Plain HTTP, and redirects must not be followed: the redirect itself is
	// the evidence, and following it would just report the portal's own 200.
	client := &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	l.step("GET %s — expecting a bare 204 with an empty body", portalURL)
	req, err := http.NewRequestWithContext(ctx, "GET", portalURL, nil)
	if err != nil {
		return Yellow, "Could not build portal probe request"
	}
	// Portals often serve cached logins; make sure we hit the network.
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")

	resp, err := client.Do(req)
	if err != nil {
		l.add("request error: %v", err)
		return Red, "No HTTP egress (portal probe failed)"
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	l.add("status         = %s", resp.Status)
	l.add("body bytes     = %d", len(body))
	l.add("Content-Type   = %s", emptyDash(resp.Header.Get("Content-Type")))
	if loc := resp.Header.Get("Location"); loc != "" {
		l.add("Location       = %s", loc)
	}

	switch {
	case resp.StatusCode == 204 && len(body) == 0:
		l.step("clean 204 — traffic is reaching the internet unintercepted")
		return Green, "No captive portal detected"
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		loc := resp.Header.Get("Location")
		l.block("portal response body", string(body))
		l.step("redirected instead of 204 — something is intercepting HTTP")
		if loc != "" {
			return Red, "Captive portal detected — sign in at " + loc
		}
		return Red, "Captive portal detected (redirected)"
	default:
		l.block("portal response body", string(body))
		l.step("unexpected response to a URL that must return an empty 204")
		return Red, fmt.Sprintf("Captive portal likely (got %s with %d bytes)", resp.Status, len(body))
	}
}
