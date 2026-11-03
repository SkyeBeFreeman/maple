package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGuard(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	h := Guard(47321, "secret-token", ok)
	cases := []struct {
		name   string
		host   string
		hdr    map[string]string
		status int
	}{
		{"ok 127", "127.0.0.1:47321", map[string]string{"Authorization": "Bearer secret-token"}, 200},
		{"ok localhost", "localhost:47321", map[string]string{"Authorization": "Bearer secret-token"}, 200},
		{"evil host", "evil.com:47321", map[string]string{"Authorization": "Bearer secret-token"}, 403},
		{"wrong port", "127.0.0.1:1", map[string]string{"Authorization": "Bearer secret-token"}, 403},
		{"origin null", "127.0.0.1:47321", map[string]string{"Authorization": "Bearer secret-token", "Origin": "null"}, 403},
		{"origin url", "127.0.0.1:47321", map[string]string{"Authorization": "Bearer secret-token", "Origin": "https://x"}, 403},
		{"origin empty", "127.0.0.1:47321", map[string]string{"Authorization": "Bearer secret-token", "Origin": ""}, 403},
		{"no token", "127.0.0.1:47321", nil, 401},
		{"wrong token", "127.0.0.1:47321", map[string]string{"Authorization": "Bearer nope"}, 401},
		{"lowercase scheme", "127.0.0.1:47321", map[string]string{"Authorization": "bearer secret-token"}, 401},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			check(t, h, c.host, c.hdr, c.status)
		})
	}

	// An unset token must not let "Bearer " through.
	check(t, Guard(47321, "", ok), "127.0.0.1:47321", map[string]string{"Authorization": "Bearer "}, 401)
}

func check(t *testing.T, h http.Handler, host string, hdr map[string]string, status int) {
	t.Helper()
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.Host = host
	for k, v := range hdr {
		r.Header[k] = []string{v}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("status %d, want %d", w.Code, status)
	}
	if status != 200 && w.Body.Len() != 0 {
		t.Fatalf("body %q", w.Body.String())
	}
}
