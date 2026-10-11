package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"linkweave/internal/platform"
	"linkweave/internal/service"
)

type testBackend struct{ updates int }

func (b *testBackend) Snapshot() service.Snapshot { return service.Snapshot{ActiveMode: "observe"} }
func (b *testBackend) Plan(c service.Config) (platform.Plan, error) {
	return platform.Plan{Mode: c.Mode}, c.Validate()
}
func (b *testBackend) Update(c service.Config) error {
	if e := c.Validate(); e != nil {
		return e
	}
	b.updates++
	return nil
}
func (b *testBackend) Wake() {}
func TestDashboardTrustBoundaries(t *testing.T) {
	token := strings.Repeat("a", 64)
	b := &testBackend{}
	s := New(b, token, "127.0.0.1:8787")
	cases := []struct {
		name, host, origin, auth, path, body string
		status                               int
	}{{"status", "127.0.0.1:8787", "", token, "/api/status", "", 200}, {"no token", "127.0.0.1:8787", "", "", "/api/status", "", 401}, {"DNS rebinding", "attacker.example", "", token, "/api/status", "", 403}, {"cross origin", "127.0.0.1:8787", "https://attacker.example", token, "/api/config", `{"mode":"balance"}`, 403}, {"invalid mode", "127.0.0.1:8787", "", token, "/api/config", `{"mode":"unknown"}`, 409}, {"unknown field", "127.0.0.1:8787", "", token, "/api/config", `{"mode":"observe","shell":"rm"}`, 400}, {"extra object", "127.0.0.1:8787", "", token, "/api/config", `{"mode":"observe"}{}`, 400}, {"valid mutation", "127.0.0.1:8787", "http://127.0.0.1:8787", token, "/api/config", `{"mode":"observe"}`, 200}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			method := "GET"
			if c.body != "" {
				method = "POST"
			}
			req := httptest.NewRequest(method, "http://"+c.host+c.path, strings.NewReader(c.body))
			req.Host = c.host
			req.Header.Set("Authorization", "Bearer "+c.auth)
			req.Header.Set("Origin", c.origin)
			req.Header.Set("Content-Type", "application/json")
			out := httptest.NewRecorder()
			s.Handler().ServeHTTP(out, req)
			if out.Code != c.status {
				t.Fatalf("status %d, want %d: %s", out.Code, c.status, out.Body.String())
			}
			if out.Header().Get("Content-Security-Policy") == "" {
				t.Fatal("missing CSP")
			}
		})
	}
	if b.updates != 1 {
		t.Fatalf("unexpected mutations: %d", b.updates)
	}
	out := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/", nil)
	s.Handler().ServeHTTP(out, req)
	if out.Code != 200 || !strings.Contains(out.Body.String(), "Network overview") {
		t.Fatal("embedded dashboard unavailable")
	}
}
