package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A cancelled run must abandon its probes promptly rather than grinding
// through every remaining network timeout, and must not report the resulting
// failures as if the network were broken.
func TestRunAllLayersHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before we even start

	done := make(chan []LayerResult, 1)
	start := time.Now()
	go func() {
		done <- RunAllLayers(ctx, RunConfig{IPv4: true, Target: "www.cloudflare.com", DNS: "1.1.1.1"})
	}()

	var layers []LayerResult
	select {
	case layers = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("RunAllLayers ignored a cancelled context and was still running after 20s")
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Errorf("cancelled run took %s; probes are not being interrupted", elapsed)
	}

	if len(layers) != 7 {
		t.Fatalf("got %d layers, want 7", len(layers))
	}
	// Nothing may be reported as a hard failure: the network was never tested.
	for _, l := range layers {
		for _, tc := range l.Tests {
			if tc.Status == Red {
				t.Errorf("layer %d %q: cancelled probe reported as Red (%q); "+
					"a cancelled run must not look like a broken network", l.Layer, tc.Name, tc.Summary)
			}
		}
	}
}

// validTarget is the guard that keeps caller-supplied values from being
// mistaken for flags by ping/traceroute/arp.
func TestValidTargetRejectsFlagsAndJunk(t *testing.T) {
	good := []string{
		"www.cloudflare.com", "1.1.1.1", "example.org", "a.b.c.d.example",
		"2606:4700:4700::1111", "::1", "host-with-dash.example.com",
	}
	for _, s := range good {
		if !validTarget(s) {
			t.Errorf("validTarget(%q) = false, want true", s)
		}
	}
	bad := []string{
		"", "-c100000", "--flood", "-f", "-I eth0",
		"host with space", "host;rm -rf /", "host|nc evil 1", "host$(id)",
		"host\nsecond", "host`id`", "host&whoami",
		strings.Repeat("a", 300),
	}
	for _, s := range bad {
		if validTarget(s) {
			t.Errorf("validTarget(%q) = true, want false", s)
		}
	}
}

func TestNormalizeOllama(t *testing.T) {
	cases := map[string]string{
		"":                     "http://127.0.0.1:11434",
		"127.0.0.1":            "http://127.0.0.1:11434",
		"192.168.1.10":         "http://192.168.1.10:11434",
		"192.168.1.10:11434":   "http://192.168.1.10:11434",
		"http://host:11434":    "http://host:11434",
		"https://host":         "https://host:11434",
		"http://host:11434/":   "http://host:11434",
		"http://host/api/tags": "http://host:11434",
		"  10.0.0.5:1234  ":    "http://10.0.0.5:1234",
		"::1":                  "http://[::1]:11434",
		"[::1]:11434":          "http://[::1]:11434",
		"2606:4700:4700::1111": "http://[2606:4700:4700::1111]:11434",
	}
	for in, want := range cases {
		if got := normalizeOllama(in); got != want {
			t.Errorf("normalizeOllama(%q) = %q, want %q", in, got, want)
		}
	}
}
