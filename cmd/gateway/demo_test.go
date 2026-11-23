package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// capture records what the demo handler hands to the chat chain.
type capture struct {
	path, auth, body, query string
	calls                   int
}

func (c *capture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	c.path, c.auth, c.body, c.query = r.URL.Path, r.Header.Get("Authorization"), string(b), r.URL.RawQuery
	c.calls++
	w.WriteHeader(http.StatusOK)
}

func post(h http.Handler, url string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, url, nil))
	return rec
}

func TestDemoNewIsUniqueAndUncacheable(t *testing.T) {
	c := &capture{}
	h := demoHandler(c, "k123")

	post(h, "/api/demo?mode=new")
	first := c.body
	post(h, "/api/demo?mode=new")

	if c.body == first {
		t.Fatal("mode=new must differ each call, or it would be answered from cache")
	}
	if strings.Contains(c.body, "temperature") {
		t.Fatal("mode=new must not set temperature 0 (that makes it cacheable)")
	}
	if c.path != "/v1/chat/completions" || c.query != "" {
		t.Fatalf("request not rewritten: path=%q query=%q", c.path, c.query)
	}
	if c.auth != "Bearer k123" {
		t.Fatalf("gateway key not attached: %q", c.auth)
	}
}

func TestDemoRepeatIsIdenticalAndCacheable(t *testing.T) {
	c := &capture{}
	h := demoHandler(c, "k")
	post(h, "/api/demo?mode=repeat")
	first := c.body
	post(h, "/api/demo?mode=repeat")
	if c.body != first {
		t.Fatal("mode=repeat must be identical each time")
	}
	if !strings.Contains(c.body, `"temperature":0`) {
		t.Fatal("mode=repeat must use temperature 0 so it can be cached")
	}
}

func TestDemoRejectsUnknownMode(t *testing.T) {
	c := &capture{}
	for _, u := range []string{"/api/demo", "/api/demo?mode=", "/api/demo?mode=evil"} {
		if rec := post(demoHandler(c, "k"), u); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: want 400, got %d", u, rec.Code)
		}
	}
	if c.calls != 0 {
		t.Fatal("a rejected request must never reach the chat chain")
	}
}

func TestDemoWithoutKeyAddsNoAuthHeader(t *testing.T) {
	c := &capture{}
	post(demoHandler(c, ""), "/api/demo?mode=new")
	if c.auth != "" {
		t.Fatalf("no key configured, yet Authorization was %q", c.auth)
	}
}

func TestDemoIgnoresCallerHeadersAndBody(t *testing.T) {
	c := &capture{}
	h := demoHandler(c, "k")
	req := httptest.NewRequest(http.MethodPost, "/api/demo?mode=new", strings.NewReader(`{"model":"gpt-evil"}`))
	req.Header.Set("Authorization", "Bearer attacker")
	req.Header.Set("X-LLM-Route", "ollama")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if strings.Contains(c.body, "gpt-evil") {
		t.Fatal("caller-supplied body must be ignored")
	}
	if c.auth != "Bearer k" {
		t.Fatalf("caller's Authorization must be replaced, got %q", c.auth)
	}
}
