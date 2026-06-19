package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
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
	mux.HandleFunc("/api/llm/models", handleLLMModels)
	mux.HandleFunc("/api/llm/analyze", handleLLMAnalyze)
	mux.HandleFunc("/api/quit", handleQuit)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Could not bind %s: %v", addr, err)
	}

	url := "http://" + ln.Addr().String() + "/"
	srv := &http.Server{Handler: mux}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	// runFrontend blocks: in the default (GUI) build it opens a native window
	// and returns when the window is closed; the headless build blocks forever.
	runFrontend(url)
}

// handleQuit lets the UI request a clean shutdown of the app.
func handleQuit(w http.ResponseWriter, r *http.Request) {
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

func handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var cfg RunConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if cfg.Target == "" {
		cfg.Target = "www.cloudflare.com"
	}
	if cfg.DNS == "" {
		cfg.DNS = "1.1.1.1"
	}
	if !cfg.IPv4 && !cfg.IPv6 {
		cfg.IPv4 = true
	}

	start := time.Now()
	layers := RunAllLayers(cfg)
	writeJSON(w, map[string]any{
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

