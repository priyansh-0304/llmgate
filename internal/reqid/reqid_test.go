package reqid

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func run(t *testing.T, incoming string) (header, inCtx string) {
	t.Helper()
	h := Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { inCtx = From(r.Context()) }))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if incoming != "" {
		req.Header.Set(Header, incoming)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Header().Get(Header), inCtx
}

func TestGeneratesIDWhenMissing(t *testing.T) {
	header, inCtx := run(t, "")
	if len(header) != 16 || header != inCtx {
		t.Errorf("header=%q ctx=%q, want matching 16-char ID", header, inCtx)
	}
}

func TestKeepsSafeClientID(t *testing.T) {
	header, inCtx := run(t, "my-trace_1.2")
	if header != "my-trace_1.2" || inCtx != header {
		t.Errorf("header=%q ctx=%q, want client ID kept", header, inCtx)
	}
}

func TestReplacesUnsafeClientID(t *testing.T) {
	for _, bad := range []string{"has space", "new\nline", "semi;colon", strings.Repeat("a", 65)} {
		header, _ := run(t, bad)
		if header == bad || len(header) != 16 {
			t.Errorf("unsafe ID %q was not replaced (got %q)", bad, header)
		}
	}
}
