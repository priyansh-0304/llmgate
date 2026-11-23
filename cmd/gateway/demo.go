package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
)

// demoHandler powers the dashboard's "Send test request" button.
//
// The browser must never hold the gateway key, so the button calls this endpoint
// instead. The handler builds one of two FIXED requests (the caller cannot choose
// the content), adds the gateway's own key, and sends it through the normal chat
// chain, so rate limiting, the token budget, caching, retries and metrics all
// apply exactly as for a real client.
//
//	?mode=new     a different prompt each time and no temperature, so it is never
//	              cached and always reaches the model
//	?mode=repeat  the same prompt with temperature 0, so the first call is a cache
//	              miss and later calls are cache hits
func demoHandler(chat http.Handler, apiKey string) http.Handler {
	var n atomic.Uint64
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body string
		switch r.URL.Query().Get("mode") {
		case "new":
			body = fmt.Sprintf(`{"max_tokens":32,"messages":[{"role":"user","content":"Demo question %d: say hello in one short sentence."}]}`, n.Add(1))
		case "repeat":
			body = `{"temperature":0,"max_tokens":32,"messages":[{"role":"user","content":"In one sentence, what is a cache?"}]}`
		default:
			http.Error(w, `{"error":{"message":"mode must be new or repeat","type":"invalid_request_error"}}`, http.StatusBadRequest)
			return
		}

		// Clone keeps the request context (so the request ID carries over).
		req := r.Clone(r.Context())
		req.URL.Path = "/v1/chat/completions"
		req.URL.RawQuery = ""
		req.Body = io.NopCloser(strings.NewReader(body))
		req.ContentLength = int64(len(body))
		req.Header = http.Header{"Content-Type": {"application/json"}}
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		chat.ServeHTTP(w, req)
	})
}
