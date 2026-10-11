package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"linkweave/internal/platform"
	"linkweave/internal/service"
)

//go:embed assets/*
var assets embed.FS

type Backend interface {
	Snapshot() service.Snapshot
	Plan(service.Config) (platform.Plan, error)
	Update(service.Config) error
	Wake()
}
type Server struct {
	backend Backend
	token   string
	host    string
	handler http.Handler
}

func Token(path string) (string, error) {
	b, e := os.ReadFile(path)
	if e == nil {
		token := strings.TrimSpace(string(b))
		if len(token) != 64 {
			return "", errors.New("invalid dashboard token file")
		}
		if _, e = hex.DecodeString(token); e != nil {
			return "", e
		}
		return token, nil
	}
	if !os.IsNotExist(e) {
		return "", e
	}
	b = make([]byte, 32)
	if _, e = rand.Read(b); e != nil {
		return "", e
	}
	token := hex.EncodeToString(b)
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return "", e
	}
	_, e = f.WriteString(token + "\n")
	ce := f.Close()
	if e == nil {
		e = ce
	}
	return token, e
}
func New(backend Backend, token, host string) *Server {
	s := &Server{backend: backend, token: token, host: host}
	mux := http.NewServeMux()
	root, _ := fs.Sub(assets, "assets")
	mux.Handle("/", http.FileServer(http.FS(root)))
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, s.backend.Snapshot()) })
	mux.HandleFunc("POST /api/plan", func(w http.ResponseWriter, r *http.Request) {
		var c service.Config
		if !decode(w, r, &c) {
			return
		}
		p, e := s.backend.Plan(c)
		if e != nil {
			respond(w, 400, map[string]string{"error": e.Error()})
			return
		}
		respond(w, 200, p)
	})
	mux.HandleFunc("POST /api/config", func(w http.ResponseWriter, r *http.Request) {
		var c service.Config
		if !decode(w, r, &c) {
			return
		}
		if e := s.backend.Update(c); e != nil {
			respond(w, 409, map[string]string{"error": e.Error()})
			return
		}
		respond(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/refresh", func(w http.ResponseWriter, r *http.Request) {
		s.backend.Wake()
		respond(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/logs", s.logs)
	s.handler = s.guard(mux)
	return s
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		respond(w, 415, map[string]string{"error": "Content-Type must be application/json"})
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if e := dec.Decode(v); e != nil {
		respond(w, 400, map[string]string{"error": "Invalid request: " + e.Error()})
		return false
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		respond(w, 400, map[string]string{"error": "Request must contain one JSON object"})
		return false
	}
	return true
}
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if r.Host != s.host {
			http.Error(w, "Unrecognized Host", http.StatusForbidden)
			return
		}
		origin := r.Header.Get("Origin")
		if origin != "" && origin != "http://"+s.host {
			http.Error(w, "Cross-origin requests are not allowed", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) != 1 {
				respond(w, 401, map[string]string{"error": "Enter the dashboard access token"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) Handler() http.Handler { return s.handler }
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	srv := &http.Server{Handler: s.handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(c)
		case <-done:
		}
	}()
	e := srv.Serve(l)
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	out, e := platform.ExecRunner{}.Run(ctx, platform.Command{Program: "journalctl", Args: []string{"--no-pager", "-n", "150", "-o", "json", "-u", "NetworkManager.service", "-u", "linkweave.service"}})
	events := []service.Event{}
	if e != nil {
		respond(w, 200, map[string]any{"events": events, "note": "Journal access unavailable; Linkweave events remain available in the dashboard"})
		return
	}
	for _, line := range strings.Split(out, "\n") {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) != nil {
			continue
		}
		message, ok := record["MESSAGE"].(string)
		if !ok {
			continue
		}
		if len(message) > 2000 {
			message = message[:2000]
		}
		unit, _ := record["_SYSTEMD_UNIT"].(string)
		events = append(events, service.Event{Time: journalTime(record["__REALTIME_TIMESTAMP"]), Level: "info", Source: unit, Message: message})
	}
	respond(w, 200, map[string]any{"events": events})
}
func journalTime(v any) time.Time {
	var n int64
	_, _ = fmt.Sscan(fmt.Sprint(v), &n)
	return time.UnixMicro(n)
}
