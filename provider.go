package main

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
)

// Supported local-LLM backends. Ollama has its own API; everything else in the
// local-model ecosystem (LM Studio, llama.cpp's server, vLLM, LocalAI, ...)
// speaks the OpenAI chat-completions shape, so one extra provider covers them
// all.
const (
	ProviderOllama = "ollama"
	ProviderOpenAI = "openai"
)

// providerDefaults maps a provider to the port it listens on by default.
var providerDefaults = map[string]string{
	ProviderOllama: "11434",
	ProviderOpenAI: "1234", // LM Studio's default
}

// normalizeProvider falls back to Ollama for empty or unknown values so an old
// client that sends no provider keeps working.
func normalizeProvider(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case ProviderOpenAI, "openai-compatible", "lmstudio", "lm-studio":
		return ProviderOpenAI
	default:
		return ProviderOllama
	}
}

// normalizeEndpoint turns user input like "192.168.1.10", "host:1234", a bare
// IPv6 literal, or a full URL into a clean scheme://host:port base URL, using
// the given provider's default port when none is supplied.
func normalizeEndpoint(host, provider string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		host = "127.0.0.1"
	}
	scheme := "http://"
	if strings.HasPrefix(host, "https://") {
		scheme = "https://"
	}
	host = strings.TrimPrefix(strings.TrimPrefix(host, "http://"), "https://")
	// Base URL only: drop any path/query and trailing slashes.
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	// Bare IPv6 literal (two-plus colons, unbracketed): bracket it so a port
	// can be attached and the URL parses.
	if strings.Count(host, ":") >= 2 && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		port := providerDefaults[normalizeProvider(provider)]
		host = net.JoinHostPort(strings.Trim(host, "[]"), port)
	}
	return scheme + host
}

// normalizeOllama is the Ollama-specific spelling of normalizeEndpoint.
func normalizeOllama(host string) string { return normalizeEndpoint(host, ProviderOllama) }

// ---- per-provider endpoint shapes ----

// modelsURL is the endpoint that lists locally available models.
func modelsURL(base, provider string) string {
	if normalizeProvider(provider) == ProviderOpenAI {
		return base + "/v1/models"
	}
	return base + "/api/tags"
}

// chatURL is the endpoint that runs a chat completion.
func chatURL(base, provider string) string {
	if normalizeProvider(provider) == ProviderOpenAI {
		return base + "/v1/chat/completions"
	}
	return base + "/api/chat"
}

// chatPayload builds the provider's request body. numCtx only exists on Ollama;
// OpenAI-compatible servers size context from the loaded model itself.
func chatPayload(provider, model, system, prompt string, numCtx int, stream bool) []byte {
	messages := []map[string]string{
		{"role": "system", "content": system},
		{"role": "user", "content": prompt},
	}
	var payload map[string]any
	if normalizeProvider(provider) == ProviderOpenAI {
		payload = map[string]any{
			"model":       model,
			"messages":    messages,
			"stream":      stream,
			"temperature": 0.2,
		}
	} else {
		payload = map[string]any{
			"model":    model,
			"messages": messages,
			"stream":   stream,
			"options": map[string]any{
				"temperature": 0.2,
				"num_ctx":     numCtx,
			},
		}
	}
	b, _ := json.Marshal(payload)
	return b
}

// parseModelList reads a provider's model listing into the shape the UI wants.
func parseModelList(body []byte, provider string) ([]map[string]any, error) {
	if normalizeProvider(provider) == ProviderOpenAI {
		var out struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("unexpected response: %w", err)
		}
		models := make([]map[string]any, 0, len(out.Data))
		for _, m := range out.Data {
			if m.ID == "" {
				continue
			}
			// The OpenAI listing carries no size or parameter metadata.
			models = append(models, map[string]any{"name": m.ID})
		}
		return models, nil
	}

	var tags struct {
		Models []struct {
			Name    string `json:"name"`
			Model   string `json:"model"`
			Size    int64  `json:"size"`
			Details struct {
				ParameterSize string `json:"parameter_size"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &tags); err != nil {
		return nil, fmt.Errorf("unexpected response: %w", err)
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
	return models, nil
}

// streamChunk is one decoded delta from a streaming chat response, normalised
// across the two provider formats.
type streamChunk struct {
	Content string
	Done    bool
	// Generation metrics, populated on the final chunk (Ollama only).
	DoneReason         string
	TotalDuration      int64
	LoadDuration       int64
	PromptEvalCount    int
	PromptEvalDuration int64
	EvalCount          int
	EvalDuration       int64
}

// parseStreamLine decodes one line of a streaming response body. ok is false
// for lines that carry no payload (blank lines, SSE comments, the [DONE]
// sentinel).
func parseStreamLine(line, provider string) (chunk streamChunk, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return chunk, false
	}
	if normalizeProvider(provider) == ProviderOpenAI {
		// OpenAI-compatible servers stream SSE frames.
		data, found := strings.CutPrefix(line, "data:")
		if !found {
			return chunk, false
		}
		data = strings.TrimSpace(data)
		// "[DONE]" is a bare terminator carrying no data. Treating it as a
		// chunk would overwrite the finish_reason captured from the frame
		// before it, so skip it — that frame already marked the stream done.
		if data == "" || data == "[DONE]" {
			return chunk, false
		}
		var f struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &f) != nil || len(f.Choices) == 0 {
			return chunk, false
		}
		c := f.Choices[0]
		chunk.Content = c.Delta.Content
		if c.FinishReason != nil && *c.FinishReason != "" {
			chunk.Done = true
			chunk.DoneReason = *c.FinishReason
		}
		return chunk, true
	}

	// Ollama streams bare newline-delimited JSON objects.
	var f struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Done               bool   `json:"done"`
		DoneReason         string `json:"done_reason"`
		TotalDuration      int64  `json:"total_duration"`
		LoadDuration       int64  `json:"load_duration"`
		PromptEvalCount    int    `json:"prompt_eval_count"`
		PromptEvalDuration int64  `json:"prompt_eval_duration"`
		EvalCount          int    `json:"eval_count"`
		EvalDuration       int64  `json:"eval_duration"`
	}
	if json.Unmarshal([]byte(line), &f) != nil {
		return chunk, false
	}
	return streamChunk{
		Content: f.Message.Content, Done: f.Done, DoneReason: f.DoneReason,
		TotalDuration: f.TotalDuration, LoadDuration: f.LoadDuration,
		PromptEvalCount: f.PromptEvalCount, PromptEvalDuration: f.PromptEvalDuration,
		EvalCount: f.EvalCount, EvalDuration: f.EvalDuration,
	}, true
}
