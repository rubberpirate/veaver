package console

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rubberpirate/veaver/internal/platform"
	"github.com/rubberpirate/veaver/internal/service"
)

func TestRenderSafeResponsiveAndPolicyDraft(t *testing.T) {
	s := service.Snapshot{ActiveMode: "observe", Config: service.DefaultConfig(), Network: platform.Network{State: "connected", Interfaces: []platform.Interface{{Name: "wlan0", Physical: true, Managed: true, Connected: true, Address: "192.168.1.2", Gateway: "192.168.1.1", Connectivity: "full", RXRate: 1024}}}, Events: []service.Event{{Message: "bad\x1b[2J\nlog"}}}
	m := model{}
	m.accept(s)
	for page := 0; page < 4; page++ {
		m.page = page
		for _, size := range [][2]int{{100, 32}, {72, 20}, {40, 12}} {
			out := m.render(size[0], size[1], false)
			if strings.Contains(out, "\x1b") {
				t.Fatal("untrusted escape reached terminal")
			}
			lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
			if len(lines) != size[1] {
				t.Fatalf("height %d, want %d", len(lines), size[1])
			}
			for _, line := range lines {
				if utf8.RuneCountInString(line) != size[0] {
					t.Fatalf("bad row width %d: %q", utf8.RuneCountInString(line), line)
				}
			}
		}
	}
	m.edit('b')
	m.edit('+')
	if m.draft.Mode != "balance" || m.draft.Weights["wlan0"] != 2 || m.snapshot.Config.Mode != "observe" {
		t.Fatal("edit modified live policy or missed draft")
	}
	m.plan = &platform.Plan{Mode: "balance"}
	m.edit('-')
	if m.plan != nil {
		t.Fatal("editing did not invalidate review")
	}
	m.accept(s)
	if m.draft.Mode != "balance" {
		t.Fatal("poll discarded unsaved draft")
	}
	if safe("\x1b]52;clipboard\a") != "?]52;clipboard?" {
		t.Fatal("terminal controls not stripped")
	}
}

func TestArrowDecoderDoesNotCancelReviewOrApplyShortcuts(t *testing.T) {
	d := keyDecoder{}
	if got := d.decode([]byte("\x1b[")); len(got) != 0 {
		t.Fatal(got)
	}
	if got := string(d.decode([]byte("B"))); got != "j" {
		t.Fatal(got)
	}
	if got := string(d.decode([]byte("\x1b[1;5A"))); got != "k" {
		t.Fatal(got)
	}
	if got := d.decode([]byte("\x1b[b")); len(got) != 0 {
		t.Fatal("escape triggered mode change", got)
	}
	d.decode([]byte{27})
	if got := d.decode(nil); len(got) != 1 || got[0] != 27 {
		t.Fatal("standalone Escape not recognized", got)
	}
}

func TestAPIClientAuthAndRejectRedirects(t *testing.T) {
	token := strings.Repeat("a", 64)
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "dashboard-token"), []byte(token), 0600); e != nil {
		t.Fatal(e)
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing auth")
		}
		if r.URL.Path == "/api/refresh" {
			http.Redirect(w, r, "/other", 302)
			return
		}
		if r.Method == "POST" && r.Header.Get("Content-Type") != "application/json" {
			t.Error("missing content type")
		}
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer s.Close()
	c, e := NewClient(s.URL, dir)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Request(context.Background(), "GET", "status", nil); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Request(context.Background(), "POST", "refresh", []byte("{}")); e == nil {
		t.Fatal("redirect accepted")
	}
	for _, address := range []string{"http://127.0.0.1:8787@evil.test", "http://127.0.0.1:8787/path", "http://127.0.0.1:8787?x=y", "https://127.0.0.1:8787", "http://localhost:8787", "http://127.0.0.1:0"} {
		if _, e = NewClient(address, dir); e == nil {
			t.Fatalf("unsafe address accepted: %s", address)
		}
	}
}
