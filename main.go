package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"
)

//go:embed web
var webFS embed.FS

// RunConfig is the client-supplied test configuration.
type RunConfig struct {
	IPv4   bool   `json:"ipv4"`
	IPv6   bool   `json:"ipv6"`
	Target string `json:"target"`
	DNS    string `json:"dns"`

	// Resolved once per run by RunAllLayers and shared across the layer
	// suites. Unexported, so never client-supplied.
	gw    string
	gwErr error
}

func main() {
	// Bind the local API server. Default to an OS-assigned ephemeral port on
	// loopback so the standalone window has no fixed-port conflicts; NSD_ADDR
	// can pin it (useful for headless/dev use).
	addr := "127.0.0.1:0"
	if v := os.Getenv("NSD_ADDR"); v != "" {
		addr = v
	}

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/api/info", handleInfo)
	mux.HandleFunc("/api/run", handleRun)
	mux.HandleFunc("/api/run/stream", handleRunStream)
	mux.HandleFunc("/api/llm/models", handleLLMModels)
	mux.HandleFunc("/api/llm/analyze", handleLLMAnalyze)
	mux.HandleFunc("/api/quit", handleQuit)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Could not bind %s: %v", addr, err)
	}

	url := "http://" + ln.Addr().String() + "/"
	srv := &http.Server{
		Handler: secureLocal(ln.Addr().String(), mux),
		// Bound slow-header (Slowloris-style) and idle connections. No
		// WriteTimeout: /api/run legitimately takes tens of seconds and
		// /api/llm/analyze up to minutes on slow local models.
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	// runFrontend blocks: in the default (GUI) build it opens a native window
	// and returns when the window is closed; the headless build blocks forever.
	runFrontend(url)
}

// secureLocal hardens the loopback API against the two ways a hostile web page
// in the user's regular browser could reach it:
//
//   - DNS rebinding: a page at evil.example re-resolves its hostname to
//     127.0.0.1 and then reads API responses as "same-origin". The Host check
//     defeats this — a rebound request still carries "Host: evil.example",
//     never our own address.
//   - Cross-site request forgery: forms and no-cors fetches can fire
//     side-effectful POSTs blind (quit the app, run diagnostics, proxy to
//     Ollama). Requiring application/json makes every API POST a non-"simple"
//     request, which browsers refuse to send cross-origin without CORS
//     approval we never grant; the Origin check backstops that.
func secureLocal(listenAddr string, next http.Handler) http.Handler {
	allowedHosts := map[string]bool{listenAddr: true}
	lhost, port, err := net.SplitHostPort(listenAddr)
	if err == nil {
		for _, h := range []string{"localhost", "127.0.0.1", "[::1]"} {
			allowedHosts[h+":"+port] = true
		}
	}
	// If NSD_ADDR deliberately binds all interfaces we cannot enumerate every
	// name clients may use, so only pin the Host on specific-address binds.
	pinHost := err == nil && lhost != "" && lhost != "0.0.0.0" && lhost != "::"

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pinHost && !allowedHosts[r.Host] {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host && o != "https://"+r.Host {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/") {
			if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// validTarget accepts hostnames, IPv4, and IPv6 literals while rejecting
// anything an external tool (ping, traceroute, tracert, arp) could misread as
// a flag. exec.Command never invokes a shell, so this is about argument
// injection ("-c 100000", "-f", ...), not command injection: the first
// character must not be "-".
var reTargetOK = regexp.MustCompile(`^[A-Za-z0-9:][A-Za-z0-9_.:\-]{0,252}$`)

func validTarget(s string) bool { return reTargetOK.MatchString(s) }

// handleQuit lets the UI request a clean shutdown of the app.
func handleQuit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusNoContent)
	go func() { requestQuit() }()
}

func handleInfo(w http.ResponseWriter, r *http.Request) {
	host, _ := os.Hostname()
	info := map[string]any{
		"os":         runtime.GOOS,
		"osLabel":    osLabel(),
		"arch":       runtime.GOARCH,
		"hostname":   host,
		"ipv6Global": hasGlobalIPv6(),
		"interfaces": interfaceSummary(),
		"defaults": map[string]string{
			"target": "www.cloudflare.com",
			"dns":    "1.1.1.1",
		},
	}
	writeJSON(w, info)
}

// decodeRunConfig reads and validates a run request body. Shared by the JSON
// and streaming endpoints so both apply the same defaults and the same guard
// against values that external tools would read as flags.
func decodeRunConfig(r *http.Request) (RunConfig, error) {
	var cfg RunConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		return cfg, err
	}
	if cfg.Target == "" {
		cfg.Target = "www.cloudflare.com"
	}
	if cfg.DNS == "" {
		cfg.DNS = "1.1.1.1"
	}
	if !validTarget(cfg.Target) {
		return cfg, errors.New("invalid target: must be a hostname or IP address")
	}
	if !validTarget(cfg.DNS) {
		return cfg, errors.New("invalid dns: must be a hostname or IP address")
	}
	if !cfg.IPv4 && !cfg.IPv6 {
		cfg.IPv4 = true
	}
	return cfg, nil
}

func handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	cfg, err := decodeRunConfig(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// r.Context() is cancelled when the client goes away — including when the
	// UI aborts the fetch — so an abandoned run stops probing instead of
	// grinding through every remaining timeout.
	start := time.Now()
	layers := RunAllLayers(r.Context(), cfg)
	writeJSON(w, map[string]any{
		"ranAt":      time.Now().Format(time.RFC3339),
		"durationMs": time.Since(start).Milliseconds(),
		"config":     cfg,
		"layers":     layers,
	})
}

// handleRunStream is the streaming twin of handleRun: it emits one Server-Sent
// Event per layer as that layer finishes, then a final "done" event carrying
// the same summary fields the JSON endpoint returns. The plain /api/run
// endpoint stays for scripted and headless use.
func handleRunStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	cfg, err := decodeRunConfig(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Without this, a proxy sitting in front would buffer the whole stream and
	// defeat the point of streaming at all.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	send := func(event string, payload any) {
		b, err := json.Marshal(payload)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		flusher.Flush()
	}

	ctx := r.Context()
	start := time.Now()
	layers := make([]LayerResult, len(layerSuites))

	send("start", map[string]any{"layers": len(layerSuites), "config": cfg})
	StreamAllLayers(ctx, cfg, func(idx int, lr LayerResult) {
		layers[idx] = lr
		send("layer", lr)
	})

	if ctx.Err() != nil {
		// The client is gone; there is nobody to receive a final event.
		return
	}
	send("done", map[string]any{
		"ranAt":      time.Now().Format(time.RFC3339),
		"durationMs": time.Since(start).Milliseconds(),
		"config":     cfg,
		"layers":     layers,
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func osLabel() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	case "linux":
		return "Linux"
	default:
		return runtime.GOOS
	}
}
