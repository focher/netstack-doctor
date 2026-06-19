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
	fullInput := llmSystemPrompt + prompt

	// Ollama's default context window (num_ctx) is only ~2048 tokens, which
	// silently truncates the full verbose diagnostics and makes the model
	// analyze only a fraction of the logs. Size num_ctx to fit the entire
	// prompt + system prompt + room for the response.
	numCtx := contextWindowFor(fullInput)

	// Request-side stats describing exactly what we're sending the model.
	reqInfo := analyzeRequestInfo(req.Config, req.Layers, prompt, numCtx)

	// Fetch model attributes (max context, params, quant, family, ...) in parallel-ish.
	modelInfo := fetchModelInfo(base, req.Model)

	payload := map[string]any{
		"model":  req.Model,
		"stream": false,
		"messages": []map[string]string{
			{"role": "system", "content": llmSystemPrompt},
			{"role": "user", "content": prompt},
		},
		"options": map[string]any{
			"temperature": 0.2,
			"num_ctx":     numCtx,
		},
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
		DoneReason         string `json:"done_reason"`
		TotalDuration      int64  `json:"total_duration"`
		LoadDuration       int64  `json:"load_duration"`
		PromptEvalCount    int    `json:"prompt_eval_count"`
		PromptEvalDuration int64  `json:"prompt_eval_duration"`
		EvalCount          int    `json:"eval_count"`
		EvalDuration       int64  `json:"eval_duration"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "could not parse model response: " + err.Error()})
		return
	}

	// Generation metrics straight from Ollama. prompt_eval_count is the exact
	// number of input tokens the model actually ingested — the proof the whole
	// log made it in (compare against the model's max context).
	genTokPerSec := 0.0
	if out.EvalDuration > 0 {
		genTokPerSec = float64(out.EvalCount) / (float64(out.EvalDuration) / 1e9)
	}
	metrics := map[string]any{
		"promptTokens":     out.PromptEvalCount,
		"responseTokens":   out.EvalCount,
		"totalMs":          out.TotalDuration / 1e6,
		"loadMs":           out.LoadDuration / 1e6,
		"promptEvalMs":     out.PromptEvalDuration / 1e6,
		"evalMs":           out.EvalDuration / 1e6,
		"genTokensPerSec":  round1(genTokPerSec),
		"doneReason":       out.DoneReason,
	}
	if modelInfo != nil && modelInfo.MaxContext > 0 && out.PromptEvalCount > 0 {
		metrics["contextUsedPct"] = round1(float64(out.PromptEvalCount) / float64(modelInfo.MaxContext) * 100)
	}

	writeJSON(w, map[string]any{
		"ok":         true,
		"endpoint":   base,
		"model":      req.Model,
		"analysis":   strings.TrimSpace(out.Message.Content),
		"durationMs": out.TotalDuration / 1e6,
		"numCtx":     numCtx,
		"request":    reqInfo,
		"modelInfo":  modelInfo,
		"metrics":    metrics,
	})
}

// modelDetails holds the attributes Ollama reports for a model via /api/show.
type modelDetails struct {
	Family       string   `json:"family"`
	Architecture string   `json:"architecture"`
	ParameterSize string  `json:"parameterSize"`
	Quantization string   `json:"quantization"`
	Format       string   `json:"format"`
	MaxContext   int      `json:"maxContext"`
	EmbedLength  int      `json:"embeddingLength"`
	Capabilities []string `json:"capabilities"`
	SizeGB       float64  `json:"sizeGB"`
}

// fetchModelInfo queries Ollama /api/show for model attributes. Returns nil on error.
func fetchModelInfo(base, model string) *modelDetails {
	reqBody, _ := json.Marshal(map[string]string{"model": model})
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Post(base+"/api/show", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	var show struct {
		Details struct {
			Family           string `json:"family"`
			Format           string `json:"format"`
			ParameterSize    string `json:"parameter_size"`
			QuantizationLevel string `json:"quantization_level"`
		} `json:"details"`
		ModelInfo    map[string]any `json:"model_info"`
		Capabilities []string       `json:"capabilities"`
	}
	if json.NewDecoder(resp.Body).Decode(&show) != nil {
		return nil
	}
	md := &modelDetails{
		Family:        show.Details.Family,
		Format:        show.Details.Format,
		ParameterSize: show.Details.ParameterSize,
		Quantization:  show.Details.QuantizationLevel,
		Capabilities:  show.Capabilities,
	}
	// model_info keys are namespaced by architecture, e.g. "qwen2.context_length".
	if arch, ok := show.ModelInfo["general.architecture"].(string); ok {
		md.Architecture = arch
	}
	for k, v := range show.ModelInfo {
		n, ok := v.(float64)
		if !ok {
			continue
		}
		switch {
		case strings.HasSuffix(k, ".context_length"):
			md.MaxContext = int(n)
		case strings.HasSuffix(k, ".embedding_length"):
			md.EmbedLength = int(n)
		}
	}
	return md
}

// analyzeRequestInfo summarizes the payload being sent to the model.
func analyzeRequestInfo(config, layers json.RawMessage, prompt string, numCtx int) map[string]any {
	nLayers, nTests, nLogLines := countDiagnostics(layers)
	approxTokens := estimateTokens(llmSystemPrompt + prompt)
	return map[string]any{
		"promptChars":      len(prompt),
		"systemPromptChars": len(llmSystemPrompt),
		"totalChars":       len(llmSystemPrompt) + len(prompt),
		"approxTokens":     approxTokens,
		"layers":           nLayers,
		"tests":            nTests,
		"logLines":         nLogLines,
		"numCtxRequested":  numCtx,
	}
}

// countDiagnostics tallies layers, tests, and total log lines in the payload.
func countDiagnostics(layers json.RawMessage) (nLayers, nTests, nLogLines int) {
	var ls []struct {
		Tests []struct {
			Logs []string `json:"logs"`
		} `json:"tests"`
	}
	if json.Unmarshal(layers, &ls) != nil {
		return
	}
	nLayers = len(ls)
	for _, l := range ls {
		nTests += len(l.Tests)
		for _, t := range l.Tests {
			nLogLines += len(t.Logs)
		}
	}
	return
}

func round1(f float64) float64 {
	return float64(int(f*10+0.5)) / 10
}

// estimateTokens approximates the token count of a string. Dense JSON with lots
// of punctuation/hex tokenizes at roughly 3 chars/token (measured ~3.05 against
// real Ollama prompt_eval_count), so we use 3 rather than the looser 4 to avoid
// under-sizing the context window.
func estimateTokens(s string) int { return len(s) / 3 }

// contextWindowFor sizes num_ctx to comfortably fit the whole prompt PLUS the
// model's reply, so neither the diagnostics log nor the response gets evicted.
// Capped at 32768 so we don't request absurd context on tiny models (Ollama
// will further clamp to the model's own trained maximum).
func contextWindowFor(prompt string) int {
	const (
		replyHeadroom = 3072
		minCtx        = 4096
		maxCtx        = 32768
		roundTo       = 2048
	)
	needed := estimateTokens(prompt) + replyHeadroom
	// round up to the next multiple of roundTo
	needed = ((needed + roundTo - 1) / roundTo) * roundTo
	if needed < minCtx {
		needed = minCtx
	}
	if needed > maxCtx {
		needed = maxCtx
	}
	return needed
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
