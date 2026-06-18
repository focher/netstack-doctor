package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// normalizeOllama turns user input like "192.168.1.10", "192.168.1.10:11434",
// or "http://host:11434" into a clean base URL.
func normalizeOllama(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		host = "127.0.0.1"
	}
	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		host = "http://" + host
	}
	host = strings.TrimRight(host, "/")
	// append default Ollama port if none specified
	rest := strings.TrimPrefix(strings.TrimPrefix(host, "http://"), "https://")
	if !strings.Contains(rest, ":") {
		host += ":11434"
	}
	return host
}

// handleLLMModels proxies Ollama's GET /api/tags so the browser avoids CORS.
func handleLLMModels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host string `json:"host"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	base := normalizeOllama(req.Host)

	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Get(base + "/api/tags")
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error(), "endpoint": base})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		writeJSON(w, map[string]any{"ok": false, "error": fmt.Sprintf("Ollama returned %s", resp.Status), "endpoint": base})
		return
	}

	var tags struct {
		Models []struct {
			Name       string `json:"name"`
			Model      string `json:"model"`
			Size       int64  `json:"size"`
			Details    struct {
				ParameterSize string `json:"parameter_size"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &tags); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "unexpected response: " + err.Error(), "endpoint": base})
		return
	}
	models := make([]map[string]any, 0, len(tags.Models))
	for _, m := range tags.Models {
		name := m.Name
		if name == "" {
			name = m.Model
		}
		models = append(models, map[string]any{
			"name":   name,
			"params": m.Details.ParameterSize,
			"sizeGB": float64(m.Size) / 1e9,
		})
	}
	writeJSON(w, map[string]any{"ok": true, "endpoint": base, "models": models})
}

// handleLLMAnalyze builds a diagnostic prompt and asks the local model to interpret it.
func handleLLMAnalyze(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host   string          `json:"host"`
		Model  string          `json:"model"`
		Layers json.RawMessage `json:"layers"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if req.Model == "" {
		writeJSON(w, map[string]any{"ok": false, "error": "no model selected"})
		return
	}
	base := normalizeOllama(req.Host)

	prompt := buildAnalysisPrompt(req.Config, req.Layers)

	payload := map[string]any{
		"model":  req.Model,
		"stream": false,
		"messages": []map[string]string{
			{"role": "system", "content": llmSystemPrompt},
			{"role": "user", "content": prompt},
		},
		"options": map[string]any{"temperature": 0.2},
	}
	buf, _ := json.Marshal(payload)

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Post(base+"/api/chat", "application/json", bytes.NewReader(buf))
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error(), "endpoint": base})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		writeJSON(w, map[string]any{"ok": false, "error": fmt.Sprintf("Ollama returned %s: %s", resp.Status, string(body))})
		return
	}

	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		TotalDuration int64 `json:"total_duration"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "could not parse model response: " + err.Error()})
		return
	}
	writeJSON(w, map[string]any{
		"ok":         true,
		"model":      req.Model,
		"analysis":   strings.TrimSpace(out.Message.Content),
		"durationMs": out.TotalDuration / 1e6,
	})
}

const llmSystemPrompt = `You are a senior network engineer reviewing automated OSI-layer diagnostics. ` +
	`Be concise and practical. Identify the most likely root cause of any failures or warnings, ` +
	`explain the impact in plain terms, and give concrete next troubleshooting steps. ` +
	`Use the layer results to reason bottom-up (a lower-layer failure often explains higher-layer ones). ` +
	`Format your answer in short Markdown sections: Summary, Likely Issues, Recommended Actions.`

func buildAnalysisPrompt(config, layers json.RawMessage) string {
	var sb strings.Builder
	sb.WriteString("Here are the network diagnostic results as JSON.\n")
	sb.WriteString("Configuration:\n")
	sb.Write(indentJSON(config))
	sb.WriteString("\n\nLayer results (each test has status green=ok, yellow=warning, red=failed, gray=skipped, plus a verbose log):\n")
	sb.Write(indentJSON(layers))
	sb.WriteString("\n\nAnalyze these results.")
	return sb.String()
}

func indentJSON(raw json.RawMessage) []byte {
	var buf bytes.Buffer
	if json.Indent(&buf, raw, "", "  ") == nil {
		return buf.Bytes()
	}
	return raw
}
