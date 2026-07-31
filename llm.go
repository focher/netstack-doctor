package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Upper bounds on how much of a proxied Ollama response we will buffer. Guards
// against a mistyped/hostile endpoint streaming an unbounded body into memory.
const (
	maxTagsBody = 1 << 20 // /api/tags, /api/show
	maxChatBody = 8 << 20 // /api/chat
)

// handleLLMModels proxies the provider's model listing so the browser avoids CORS.
func handleLLMModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Host     string `json:"host"`
		Provider string `json:"provider"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	provider := normalizeProvider(req.Provider)
	base := normalizeEndpoint(req.Host, provider)
	label := providerLabel(provider)

	client := &http.Client{Timeout: 6 * time.Second}
	httpReq, err := http.NewRequestWithContext(r.Context(), "GET", modelsURL(base, provider), nil)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error(), "endpoint": base})
		return
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error(), "endpoint": base})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxTagsBody))
	if resp.StatusCode != 200 {
		writeJSON(w, map[string]any{"ok": false, "error": fmt.Sprintf("%s returned %s", label, resp.Status), "endpoint": base})
		return
	}

	models, err := parseModelList(body, provider)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error(), "endpoint": base})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "endpoint": base, "provider": provider, "models": models})
}

// providerLabel is the human-facing name used in error messages.
func providerLabel(provider string) string {
	if normalizeProvider(provider) == ProviderOpenAI {
		return "The OpenAI-compatible server"
	}
	return "Ollama"
}

// handleLLMAnalyze builds a diagnostic prompt and asks the local model to interpret it.
func handleLLMAnalyze(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Host     string          `json:"host"`
		Provider string          `json:"provider"`
		Model    string          `json:"model"`
		Layers   json.RawMessage `json:"layers"`
		Config   json.RawMessage `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if req.Model == "" {
		writeJSON(w, map[string]any{"ok": false, "error": "no model selected"})
		return
	}
	provider := normalizeProvider(req.Provider)
	base := normalizeEndpoint(req.Host, provider)

	prompt := buildAnalysisPrompt(req.Config, req.Layers)
	fullInput := llmSystemPrompt + prompt

	// Ollama's default context window (num_ctx) is only ~2048 tokens, which
	// silently truncates the full verbose diagnostics and makes the model
	// analyze only a fraction of the logs. Size num_ctx to fit the entire
	// prompt + system prompt + room for the response.
	numCtx := contextWindowFor(fullInput)

	// Request-side stats describing exactly what we're sending the model.
	reqInfo := analyzeRequestInfo(req.Config, req.Layers, prompt, numCtx)

	// Fetch model attributes (max context, params, quant, family, ...) in
	// parallel with the chat request — they are only needed for the metrics
	// block after generation completes, so don't pay for the /api/show
	// round-trip up front.
	infoCh := make(chan *modelDetails, 1)
	go func() { infoCh <- fetchModelInfo(r.Context(), base, provider, req.Model) }()

	buf := chatPayload(provider, req.Model, llmSystemPrompt, prompt, numCtx, true)

	client := &http.Client{Timeout: 10 * time.Minute}
	httpReq, err := http.NewRequestWithContext(r.Context(), "POST", chatURL(base, provider), bytes.NewReader(buf))
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error(), "endpoint": base})
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(httpReq)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error(), "endpoint": base})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		writeJSON(w, map[string]any{"ok": false,
			"error": fmt.Sprintf("%s returned %s: %s", providerLabel(provider), resp.Status, string(body))})
		return
	}

	// Past this point the response is a stream, so switch to SSE. Errors from
	// here on are delivered as an "error" event rather than a JSON body.
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, map[string]any{"ok": false, "error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
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
	send("start", map[string]any{
		"endpoint": base, "provider": provider, "model": req.Model,
		"numCtx": numCtx, "request": reqInfo,
	})

	start := time.Now()
	var full strings.Builder
	var final streamChunk

	// Providers stream newline-delimited chunks; a single log line can be long,
	// so give the scanner room well beyond bufio's 64KB default.
	sc := bufio.NewScanner(io.LimitReader(resp.Body, maxChatBody))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		chunk, ok := parseStreamLine(sc.Text(), provider)
		if !ok {
			continue
		}
		if chunk.Content != "" {
			full.WriteString(chunk.Content)
			send("token", map[string]any{"t": chunk.Content})
		}
		if chunk.Done {
			final = chunk
		}
	}
	if err := sc.Err(); err != nil {
		send("error", map[string]any{"error": "stream interrupted: " + err.Error()})
		return
	}

	modelInfo := <-infoCh

	// Generation metrics. prompt_eval_count is the exact number of input tokens
	// the model actually ingested — the proof the whole log made it in (compare
	// against the model's max context). OpenAI-compatible servers do not report
	// these, so the block is simply thinner there.
	genTokPerSec := 0.0
	if final.EvalDuration > 0 {
		genTokPerSec = float64(final.EvalCount) / (float64(final.EvalDuration) / 1e9)
	}
	totalMs := final.TotalDuration / 1e6
	if totalMs == 0 {
		totalMs = time.Since(start).Milliseconds()
	}
	metrics := map[string]any{
		"promptTokens":    final.PromptEvalCount,
		"responseTokens":  final.EvalCount,
		"totalMs":         totalMs,
		"loadMs":          final.LoadDuration / 1e6,
		"promptEvalMs":    final.PromptEvalDuration / 1e6,
		"evalMs":          final.EvalDuration / 1e6,
		"genTokensPerSec": round1(genTokPerSec),
		"doneReason":      final.DoneReason,
	}
	if modelInfo != nil && modelInfo.MaxContext > 0 && final.PromptEvalCount > 0 {
		metrics["contextUsedPct"] = round1(float64(final.PromptEvalCount) / float64(modelInfo.MaxContext) * 100)
	}

	send("done", map[string]any{
		"ok":         true,
		"endpoint":   base,
		"provider":   provider,
		"model":      req.Model,
		"analysis":   strings.TrimSpace(full.String()),
		"durationMs": totalMs,
		"numCtx":     numCtx,
		"request":    reqInfo,
		"modelInfo":  modelInfo,
		"metrics":    metrics,
	})
}

// modelDetails holds the attributes Ollama reports for a model via /api/show.
type modelDetails struct {
	Family        string   `json:"family"`
	Architecture  string   `json:"architecture"`
	ParameterSize string   `json:"parameterSize"`
	Quantization  string   `json:"quantization"`
	Format        string   `json:"format"`
	MaxContext    int      `json:"maxContext"`
	EmbedLength   int      `json:"embeddingLength"`
	Capabilities  []string `json:"capabilities"`
	SizeGB        float64  `json:"sizeGB"`
}

// fetchModelInfo queries Ollama /api/show for model attributes. Returns nil on
// error, and for OpenAI-compatible servers, which expose no equivalent.
func fetchModelInfo(ctx context.Context, base, provider, model string) *modelDetails {
	if normalizeProvider(provider) == ProviderOpenAI {
		return nil
	}
	reqBody, _ := json.Marshal(map[string]string{"model": model})
	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/api/show", bytes.NewReader(reqBody))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	var show struct {
		Details struct {
			Family            string `json:"family"`
			Format            string `json:"format"`
			ParameterSize     string `json:"parameter_size"`
			QuantizationLevel string `json:"quantization_level"`
		} `json:"details"`
		ModelInfo    map[string]any `json:"model_info"`
		Capabilities []string       `json:"capabilities"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, maxTagsBody)).Decode(&show) != nil {
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
		"promptChars":       len(prompt),
		"systemPromptChars": len(llmSystemPrompt),
		"totalChars":        len(llmSystemPrompt) + len(prompt),
		"approxTokens":      approxTokens,
		"layers":            nLayers,
		"tests":             nTests,
		"logLines":          nLogLines,
		"numCtxRequested":   numCtx,
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

const llmSystemPrompt = `You are a senior network engineer reviewing automated OSI-layer diagnostics.

STATUS VALUES — read each test's "status" field exactly. Do not invent or infer status:
- green = passed / healthy.
- yellow = warning or degraded, but still functional.
- red = FAILED. These are the real problems.
- gray = SKIPPED / not applicable — the test did NOT run (e.g. IPv6 probes skipped because the host has no global IPv6 address). A gray test is NOT a pass and NOT a failure. Never describe a gray test as "working", "healthy", "successful", or "green". State that it was skipped and why (read its "summary"/logs for the reason). Do not recommend fixes for skipped tests beyond noting the precondition (e.g. "no IPv6 connectivity to test").

LAYER NUMBERING — use the OSI layer number and name exactly as given in each layer's "layer" and "name" fields. The seven layers are fixed: 1 Physical, 2 Data Link, 3 Network, 4 Transport, 5 Session, 6 Presentation, 7 Application. Never renumber, merge, skip, or rename a layer, and never put a test under the wrong layer. If you reference a finding, cite it as "Layer N (Name)" matching the data. Do not combine two layers into one heading.

REASONING:
- Reason bottom-up: a lower-layer red often explains higher-layer reds, so identify the deepest failing layer first.
- Base every claim on the actual status fields and logs provided — do not assume a layer passed if its data isn't green.
- If there are no red or yellow tests, say so plainly rather than inventing issues.

OUTPUT — concise Markdown with exactly these sections:
### Summary  (2-3 sentences: overall health, note which layers were skipped and why)
### Likely Issues  (list EVERY red and EVERY yellow test — one bullet each, do not omit or merge any, even minor ones; tag each "Layer N (Name): test name (red/yellow) — reason"; write "None" only if there are zero red and zero yellow tests)
### Recommended Actions  (concrete next steps for the real issues; omit actions for skipped tests)`

func buildAnalysisPrompt(config, layers json.RawMessage) string {
	var sb strings.Builder
	sb.WriteString("Here are the network diagnostic results as JSON.\n")
	sb.WriteString("Configuration:\n")
	sb.Write(indentJSON(config))
	sb.WriteString("\n\nLayer results follow. Each layer has a fixed \"layer\" number (1-7) and \"name\"; each test has a \"status\" (green=ok, yellow=warning, red=failed, gray=SKIPPED/not-run) and a verbose log:\n")
	sb.Write(indentJSON(layers))

	// Deterministic checklist: the app already knows exactly which tests are
	// flagged, so spell them out. Small models reliably cover an explicit list
	// but often miss items when asked to scan the full JSON themselves.
	flagged, skipped := flaggedAndSkipped(layers)
	sb.WriteString("\n\nCHECKLIST — these are the ONLY non-green tests. Your \"Likely Issues\" section must contain exactly one bullet for EACH of these (no more, no fewer):\n")
	if len(flagged) == 0 {
		sb.WriteString("  (none — there are no red or yellow tests; write \"None\")\n")
	} else {
		for _, f := range flagged {
			sb.WriteString("  - " + f + "\n")
		}
	}
	if len(skipped) > 0 {
		sb.WriteString("Skipped (gray) tests — mention these were skipped in the Summary, but do NOT list them as issues and do NOT call them healthy:\n")
		for _, s := range skipped {
			sb.WriteString("  - " + s + "\n")
		}
	}

	sb.WriteString("\nAnalyze these results. Treat gray as skipped (not a pass), keep the exact layer numbers/names, and cover every checklist item.")
	return sb.String()
}

// flaggedAndSkipped returns human-readable "Layer N (Name): test (status) — summary"
// lines for red/yellow tests, and a separate list for gray/skipped tests.
func flaggedAndSkipped(layers json.RawMessage) (flagged, skipped []string) {
	var ls []struct {
		Layer int    `json:"layer"`
		Name  string `json:"name"`
		Tests []struct {
			Name    string `json:"name"`
			Status  string `json:"status"`
			Summary string `json:"summary"`
		} `json:"tests"`
	}
	if json.Unmarshal(layers, &ls) != nil {
		return
	}
	for _, l := range ls {
		for _, t := range l.Tests {
			line := fmt.Sprintf("Layer %d (%s): %s (%s) — %s", l.Layer, l.Name, t.Name, t.Status, t.Summary)
			switch t.Status {
			case "red", "yellow":
				flagged = append(flagged, line)
			case "gray":
				skipped = append(skipped, fmt.Sprintf("Layer %d (%s): %s — %s", l.Layer, l.Name, t.Name, t.Summary))
			}
		}
	}
	return
}

func indentJSON(raw json.RawMessage) []byte {
	var buf bytes.Buffer
	if json.Indent(&buf, raw, "", "  ") == nil {
		return buf.Bytes()
	}
	return raw
}
