// Package httpx has tiny HTTP helpers shared by several packages.
package httpx

import (
	"encoding/json"
	"net/http"
)

// WriteError sends an OpenAI-style JSON error body, so OpenAI client libraries
// can parse errors coming from the gateway.
func WriteError(w http.ResponseWriter, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errType,
		},
	})
}
