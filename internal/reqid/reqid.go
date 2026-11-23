// Package reqid gives every request an ID (the X-Request-ID header).
//
// With an ID you can follow one request across the client, the gateway logs
// and the upstream's logs. A caller may supply its own ID; otherwise we make one.
package reqid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

const Header = "X-Request-ID"

// ctxKey is a private type so no other package can collide with our context key.
type ctxKey struct{}

// From returns the request ID stored in ctx ("" if none).
func From(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// Middleware reads or creates the ID, stores it in the request context and
// echoes it in the response headers.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(Header)
		if !valid(id) {
			id = newID()
		}
		w.Header().Set(Header, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	})
}

// valid accepts short IDs made of safe characters only. Client-supplied values
// end up in logs and upstream headers, so we never trust arbitrary input.
func valid(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b)
}
