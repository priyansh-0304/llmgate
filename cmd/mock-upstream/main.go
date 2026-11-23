// Command mock-upstream is a fake OpenAI-compatible chat server for load
// testing. It never calls any external service, so you can hammer it without
// cost or real 429s. Latency, jitter, error rate and output length are tunable.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// profile is how one model name behaves.
type profile struct {
	Latency    time.Duration // fixed delay before the first byte
	Jitter     time.Duration // plus a random 0..Jitter
	ErrorRate  float64       // probability of answering 500
	Tokens     int           // completion tokens to "generate"
	TokenDelay time.Duration // per-token delay when streaming
}

var counter atomic.Uint64

func main() {
	base := profile{
		Latency:    envDuration("MOCK_LATENCY", 20*time.Millisecond),
		Jitter:     envDuration("MOCK_JITTER", 10*time.Millisecond),
		ErrorRate:  envFloat("MOCK_ERROR_RATE", 0),
		Tokens:     int(envFloat("MOCK_COMPLETION_TOKENS", 64)),
		TokenDelay: envDuration("MOCK_TOKEN_DELAY", 5*time.Millisecond),
	}
	// Named presets. Any other model name uses the env-configured base profile.
	profiles := map[string]profile{
		"mock-fast":  base,
		"mock-slow":  {Latency: 800 * time.Millisecond, Jitter: 400 * time.Millisecond, Tokens: base.Tokens, TokenDelay: 20 * time.Millisecond},
		"mock-flaky": {Latency: 100 * time.Millisecond, Jitter: 50 * time.Millisecond, ErrorRate: 0.1, Tokens: base.Tokens, TokenDelay: base.TokenDelay},
		// Always fails: use it to watch retries, the circuit breaker and fallbacks.
		"mock-down": {Latency: 5 * time.Millisecond, ErrorRate: 1, Tokens: base.Tokens, TokenDelay: base.TokenDelay},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		handleChat(w, r, base, profiles)
	})

	addr := envStr("LISTEN_ADDR", ":9000")
	log.Printf("mock upstream listening on %s (base latency=%v jitter=%v tokens=%d)", addr, base.Latency, base.Jitter, base.Tokens)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

type chatRequest struct {
	Model         string `json:"model"`
	Stream        bool   `json:"stream"`
	MaxTokens     int    `json:"max_tokens"`
	StreamOptions struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
	Messages []struct {
		Content string `json:"content"`
	} `json:"messages"`
}

func handleChat(w http.ResponseWriter, r *http.Request, base profile, profiles map[string]profile) {
	var req chatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, `{"error":{"message":"bad json","type":"invalid_request_error"}}`, http.StatusBadRequest)
		return
	}
	p, ok := profiles[req.Model]
	if !ok {
		p = base
	}

	// Simulate time-to-first-byte.
	delay := p.Latency
	if p.Jitter > 0 {
		delay += time.Duration(rand.Int64N(int64(p.Jitter)))
	}
	select {
	case <-time.After(delay):
	case <-r.Context().Done():
		return
	}

	if p.ErrorRate > 0 && rand.Float64() < p.ErrorRate {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"simulated upstream failure","type":"server_error"}}`))
		return
	}

	completionTokens := p.Tokens
	if req.MaxTokens > 0 && req.MaxTokens < completionTokens {
		completionTokens = req.MaxTokens
	}
	promptChars := 0
	for _, m := range req.Messages {
		promptChars += utf8.RuneCountInString(m.Content)
	}
	promptTokens := (promptChars + 3) / 4
	id := fmt.Sprintf("chatcmpl-mock-%d", counter.Add(1))
	created := time.Now().Unix()

	// One word = one "token" in this fake model, so usage numbers are exact.
	words := make([]string, completionTokens)
	for i := range words {
		words[i] = "word" + strconv.Itoa(i%10)
	}

	if !req.Stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": id, "object": "chat.completion", "created": created, "model": req.Model,
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": strings.Join(words, " ")},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{
				"prompt_tokens": promptTokens, "completion_tokens": completionTokens,
				"total_tokens": promptTokens + completionTokens,
			},
		})
		return
	}

	// Streaming (SSE).
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	rc := http.NewResponseController(w)
	send := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		_ = rc.Flush()
	}
	for i, word := range words {
		if i > 0 {
			word = " " + word
		}
		send(map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": req.Model,
			"choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": word}}},
		})
		select {
		case <-time.After(p.TokenDelay):
		case <-r.Context().Done():
			return
		}
	}
	if req.StreamOptions.IncludeUsage {
		send(map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": req.Model,
			"choices": []any{},
			"usage": map[string]int{
				"prompt_tokens": promptTokens, "completion_tokens": completionTokens,
				"total_tokens": promptTokens + completionTokens,
			},
		})
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	_ = rc.Flush()
}

// ---- tiny env helpers ----

func envStr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envFloat(k string, def float64) float64 {
	if v := os.Getenv(k); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
		log.Fatalf("invalid %s=%q", k, v)
	}
	return def
}

func envDuration(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		log.Fatalf("invalid %s=%q (use e.g. 50ms)", k, v)
	}
	return def
}
