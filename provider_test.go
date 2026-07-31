package main

import "testing"

func TestNormalizeEndpointUsesProviderDefaultPort(t *testing.T) {
	cases := []struct{ host, provider, want string }{
		{"", ProviderOllama, "http://127.0.0.1:11434"},
		{"", ProviderOpenAI, "http://127.0.0.1:1234"},
		{"host", ProviderOpenAI, "http://host:1234"},
		{"host:9999", ProviderOpenAI, "http://host:9999"},
		{"https://host/v1", ProviderOpenAI, "https://host:1234"},
		{"::1", ProviderOpenAI, "http://[::1]:1234"},
		// Unknown providers fall back to Ollama rather than producing a portless URL.
		{"host", "nonsense", "http://host:11434"},
	}
	for _, c := range cases {
		if got := normalizeEndpoint(c.host, c.provider); got != c.want {
			t.Errorf("normalizeEndpoint(%q, %q) = %q, want %q", c.host, c.provider, got, c.want)
		}
	}
}

func TestParseStreamLineOllama(t *testing.T) {
	c, ok := parseStreamLine(`{"message":{"content":"hi"},"done":false}`, ProviderOllama)
	if !ok || c.Content != "hi" || c.Done {
		t.Fatalf("content chunk: got %+v ok=%v", c, ok)
	}
	c, ok = parseStreamLine(`{"message":{"content":""},"done":true,"done_reason":"stop","eval_count":42}`, ProviderOllama)
	if !ok || !c.Done || c.DoneReason != "stop" || c.EvalCount != 42 {
		t.Fatalf("final chunk: got %+v ok=%v", c, ok)
	}
	if _, ok := parseStreamLine("", ProviderOllama); ok {
		t.Error("blank line should not yield a chunk")
	}
	if _, ok := parseStreamLine("not json", ProviderOllama); ok {
		t.Error("unparseable line should not yield a chunk")
	}
}

func TestParseStreamLineOpenAI(t *testing.T) {
	c, ok := parseStreamLine(`data: {"choices":[{"delta":{"content":"hi"},"finish_reason":null}]}`, ProviderOpenAI)
	if !ok || c.Content != "hi" || c.Done {
		t.Fatalf("content chunk: got %+v ok=%v", c, ok)
	}
	c, ok = parseStreamLine(`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`, ProviderOpenAI)
	if !ok || !c.Done || c.DoneReason != "stop" {
		t.Fatalf("final chunk: got %+v ok=%v", c, ok)
	}
	// The bare terminator must not be treated as a chunk: doing so would
	// overwrite the finish_reason carried by the frame before it.
	if _, ok := parseStreamLine("data: [DONE]", ProviderOpenAI); ok {
		t.Error("[DONE] sentinel should not yield a chunk")
	}
	if _, ok := parseStreamLine(": keep-alive comment", ProviderOpenAI); ok {
		t.Error("SSE comment should not yield a chunk")
	}
}

func TestChatPayloadShape(t *testing.T) {
	ollama := string(chatPayload(ProviderOllama, "m", "sys", "user", 8192, true))
	if !contains(ollama, `"num_ctx":8192`) || !contains(ollama, `"stream":true`) {
		t.Errorf("ollama payload missing num_ctx/stream: %s", ollama)
	}
	// num_ctx is Ollama-specific; sending it to an OpenAI-compatible server
	// would be rejected as an unknown field by strict implementations.
	openai := string(chatPayload(ProviderOpenAI, "m", "sys", "user", 8192, true))
	if contains(openai, "num_ctx") {
		t.Errorf("openai payload must not carry num_ctx: %s", openai)
	}
	if !contains(openai, `"temperature":0.2`) {
		t.Errorf("openai payload missing temperature: %s", openai)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
