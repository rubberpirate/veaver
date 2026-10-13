package console

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/rubberpirate/veaver/internal/platform"
	"github.com/rubberpirate/veaver/internal/service"
)

type model struct {
	snapshot             service.Snapshot
	draft                service.Config
	page, cursor, scroll int
	dirty, loaded, busy  bool
	note                 string
	plan                 *platform.Plan
	journal              []service.Event
}

func cloneConfig(c service.Config) service.Config {
	c.Interfaces = slices.Clone(c.Interfaces)
	c.Weights = maps.Clone(c.Weights)
	return c
}

func (m *model) accept(s service.Snapshot) {
	first := m.snapshot.Time.IsZero()
	m.snapshot = s
	links := m.uplinks()
	if first {
		for i, d := range links {
			if platform.Eligible(d, s.Config.AllowMetered) {
				m.cursor = i
				break
			}
		}
	}
	if !m.loaded || !m.dirty {
		m.draft = cloneConfig(s.Config)
	}
	m.loaded = true
	if m.cursor >= len(links) {
		m.cursor = max(0, len(links)-1)
	}
}

func (m *model) uplinks() []platform.Interface {
	links := []platform.Interface{}
	for _, d := range m.snapshot.Network.Interfaces {
		if d.Physical {
			links = append(links, d)
		}
	}
	return links
}

// Edits affect only the draft. Applying always needs a successful review first.
func (m *model) edit(key byte) {
	m.plan = nil
	switch key {
	case 'o':
		m.draft.Mode = "observe"
	case 'b':
		m.draft.Mode = "balance"
	case 'r':
		m.draft.Mode = "relay"
	case 'm':
		m.draft.AllowMetered = !m.draft.AllowMetered
	case 'u':
		m.draft.Interfaces = []string{}
	case ' ', '+', '-', '=':
		links := m.uplinks()
		if m.cursor >= len(links) {
			return
		}
		d := links[m.cursor]
		if !d.Physical {
			m.note = "Choose a physical uplink"
			return
		}
		if key == ' ' {
			if len(m.draft.Interfaces) == 0 {
				for _, link := range m.snapshot.Network.Interfaces {
					if platform.Eligible(link, m.draft.AllowMetered) {
						m.draft.Interfaces = append(m.draft.Interfaces, link.Name)
					}
				}
			}
			if index := slices.Index(m.draft.Interfaces, d.Name); index >= 0 {
				if len(m.draft.Interfaces) == 1 {
					m.note = "Last selected link retained; use Observe to stop routing"
					return
				}
				m.draft.Interfaces = slices.Delete(m.draft.Interfaces, index, index+1)
			} else {
				m.draft.Interfaces = append(m.draft.Interfaces, d.Name)
			}
		} else {
			if m.draft.Weights == nil {
				m.draft.Weights = map[string]int{}
			}
			weight := linkWeight(m.draft, d.Name)
			if key == '-' {
				weight--
			} else {
				weight++
			}
			m.draft.Weights[d.Name] = min(100, max(1, weight))
		}
	default:
		return
	}
	m.dirty = true
	m.note = "Draft changed. Press a to review, then Enter to apply."
}

type result struct {
	action string
	data   []byte
	err    error
}

func Run(ctx context.Context, c *Client, once bool, draft *service.Config) error {
	if once {
		b, e := c.Request(ctx, "GET", "status", nil)
		if e != nil {
			return e
		}
		var m model
		var s service.Snapshot
		if e = json.Unmarshal(b, &s); e != nil {
			return e
		}
		m.accept(s)
		fmt.Print(m.render(100, 32, false))
		return nil
	}
	restore, err := terminal()
	if err != nil {
		return err
	}
	defer restore()
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	keys := make(chan byte, 32)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		var b [32]byte
		decoder := keyDecoder{}
		for ctx.Err() == nil {
			n, e := syscall.Read(int(os.Stdin.Fd()), b[:])
			if e != nil && e != syscall.EINTR {
				return
			}
			for _, key := range decoder.decode(b[:max(0, n)]) {
				select {
				case keys <- key:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-readerDone }()
	responses := make(chan result, 1)
	m := model{note: "Connecting to " + c.URL}
	if draft != nil {
		m.draft = cloneConfig(*draft)
		m.dirty = true
		m.loaded = true
		m.page = 3
		m.note = "Loaded draft. Press a to review before applying."
	}
	request := func(action, method, path string, body []byte) {
		m.busy = true
		go func() {
			requestCtx, stop := context.WithTimeout(ctx, 60*time.Second)
			if action == "status" || action == "logs" {
				stop()
				requestCtx, stop = context.WithTimeout(ctx, 5*time.Second)
			}
			defer stop()
			b, e := c.Request(requestCtx, method, path, body)
			select {
			case responses <- result{action, b, e}:
			case <-ctx.Done():
			}
		}()
	}
	request("status", "GET", "status", nil)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		w, h := dimensions()
		fmt.Fprint(os.Stdout, "\x1b[H"+m.render(w, h, true))
		select {
		case <-ctx.Done():
			return nil
		case <-readerDone:
			return nil
		case <-ticker.C:
			if !m.busy {
				request("status", "GET", "status", nil)
			}
		case response := <-responses:
			m.busy = false
			if response.err != nil {
				m.note = response.err.Error()
				if response.action == "plan" {
					m.plan = nil
				}
				continue
			}
			switch response.action {
			case "status":
				var s service.Snapshot
				if e := json.Unmarshal(response.data, &s); e != nil {
					m.note = e.Error()
				} else {
					m.accept(s)
					if m.note == "Connecting to "+c.URL {
						m.note = "Live NIC counters include all traffic and protocol overhead"
					}
				}
			case "plan":
				var p platform.Plan
				if e := json.Unmarshal(response.data, &p); e != nil {
					m.note = e.Error()
				} else {
					m.plan = &p
					m.scroll = 0
					m.note = "Review warnings and commands. Enter applies; Esc cancels."
				}
			case "config":
				m.dirty = false
				m.plan = nil
				m.note = "Policy applied. Refreshing actual state..."
				request("status", "GET", "status", nil)
			case "refresh":
				m.note = "NetworkManager refresh requested"
			case "logs":
				var logs struct {
					Events []service.Event `json:"events"`
					Note   string          `json:"note"`
				}
				if e := json.Unmarshal(response.data, &logs); e != nil {
					m.note = e.Error()
				} else {
					m.journal = logs.Events
					m.note = logs.Note
				}
			}
		case key := <-keys:
			if key == 27 {
				m.plan = nil
				m.note = "Review canceled"
				continue
			}
			if key == 'q' {
				return nil
			}
			if key >= '1' && key <= '4' {
				m.page = int(key - '1')
				m.scroll = 0
				continue
			}
			if key == '\t' {
				m.page = (m.page + 1) % 4
				m.scroll = 0
				continue
			}
			if key == 'j' || key == 'k' {
				delta := 1
				if key == 'k' {
					delta = -1
				}
				if m.plan != nil || m.page == 1 || m.page == 2 {
					m.scroll = max(0, m.scroll+delta)
				} else {
					m.cursor = min(max(0, len(m.uplinks())-1), max(0, m.cursor+delta))
				}
				continue
			}
			if m.busy {
				continue
			}
			if key == 'f' {
				request("refresh", "POST", "refresh", []byte("{}"))
				continue
			}
			if key == 'l' {
				m.page = 2
				m.scroll = 0
				request("logs", "GET", "logs", nil)
				continue
			}
			if !m.loaded {
				continue
			}
			if key == '\r' || key == '\n' {
				if m.plan != nil {
					b, _ := json.Marshal(m.draft)
					m.plan = nil
					m.note = "Applying policy..."
					request("config", "POST", "config", b)
				}
				continue
			}
			if key == 'a' && m.page == 3 {
				b, _ := json.Marshal(m.draft)
				m.note = "Validating policy..."
				request("plan", "POST", "plan", b)
				continue
			}
			if key == 'x' && m.page == 3 {
				m.draft = cloneConfig(m.snapshot.Config)
				m.dirty = false
				m.plan = nil
				m.note = "Draft reset to daemon configuration"
				continue
			}
			if m.page == 3 {
				m.edit(key)
			}
		}
	}
}

type keyDecoder struct{ escape int }

func (d *keyDecoder) decode(input []byte) []byte {
	out := []byte{}
	if len(input) == 0 && d.escape != 0 {
		if d.escape == 1 {
			out = append(out, 27)
		}
		d.escape = 0
	}
	for _, key := range input {
		switch d.escape {
		case 1:
			if key == '[' || key == 'O' {
				d.escape = 2
				continue
			}
			out = append(out, 27)
			d.escape = 0
		case 2:
			if key >= '0' && key <= '9' || key == ';' {
				continue
			}
			d.escape = 0
			if key == 'A' {
				out = append(out, 'k')
			} else if key == 'B' {
				out = append(out, 'j')
			}
			continue
		}
		if key == 27 {
			d.escape = 1
		} else {
			out = append(out, key)
		}
	}
	return out
}
