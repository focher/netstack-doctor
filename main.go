package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
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
	addr := "127.0.0.1:8696"
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

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Could not bind %s: %v", addr, err)
	}

	url := "http://" + addr + "/"
	fmt.Println("NetStack Doctor running at", url)
	fmt.Println("Close this window (or press Ctrl+C) to quit.")
	go openBrowser(url)

	srv := &http.Server{Handler: mux}
	log.Fatal(srv.Serve(ln))
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

func openBrowser(url string) {
	time.Sleep(400 * time.Millisecond)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
