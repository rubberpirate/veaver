package console

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/rubberpirate/veaver/internal/platform"
	"github.com/rubberpirate/veaver/internal/service"
)

// Interface names, journal data and API errors must never become terminal commands.
func safe(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r > 126 {
			return '?'
		}
		return r
	}, s)
}
func fit(s string, width int) string {
	r := []rune(s)
	if width <= 0 {
		return ""
	}
	if len(r) > width {
		return string(r[:width])
	}
	return s + strings.Repeat(" ", width-len(r))
}
func rate(n float64) string {
	if n < 0 || math.IsNaN(n) || math.IsInf(n, 0) {
		n = 0
	}
	units := []string{"B/s", "KiB/s", "MiB/s", "GiB/s"}
	i := 0
	for n >= 1024 && i < len(units)-1 {
		n /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", n, units[i])
}
func volume(n uint64) string { return strings.TrimSuffix(rate(float64(n)), "/s") }
func linkWeight(c service.Config, name string) int {
	if w := c.Weights[name]; w > 0 {
		return w
	}
	return 1
}
func chosen(c service.Config, d platform.Interface) bool {
	if len(c.Interfaces) == 0 {
		return platform.Eligible(d, c.AllowMetered)
	}
	for _, name := range c.Interfaces {
		if name == d.Name {
			return true
		}
	}
	return false
}
func spark(values []float64, width int) string {
	if width <= 0 {
		return ""
	}
	if len(values) > width {
		values = values[len(values)-width:]
	}
	peak := 1.0
	for _, v := range values {
		if v > peak {
			peak = v
		}
	}
	bars := []rune("▁▂▃▄▅▆▇█")
	var out strings.Builder
	out.WriteString(strings.Repeat(" ", width-len(values)))
	for _, v := range values {
		index := 0
		if v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0) {
			index = min(7, int(v/peak*7))
		}
		out.WriteRune(bars[index])
	}
	return out.String()
}
func box(title string, rows []string, width int) []string {
	width = max(4, width)
	label := " " + title + " "
	top := "╭" + fit(label, width-2) + "╮"
	out := []string{top}
	for _, row := range rows {
		out = append(out, "│"+fit(row, width-2)+"│")
	}
	return append(out, "╰"+strings.Repeat("─", width-2)+"╯")
}
func wrap(s string, width int) []string {
	width = max(1, width)
	r := []rune(s)
	out := []string{}
	for len(r) > width {
		out = append(out, string(r[:width]))
		r = r[width:]
	}
	return append(out, string(r))
}
func (m *model) interfaces(width int, policy bool) []string {
	links := m.uplinks()
	cfg := m.snapshot.Config
	if policy {
		cfg = m.draft
	}
	rows := []string{fmt.Sprintf("  %-15s %-10s %12s %12s %6s  %s", "UPLINK", "HEALTH", "DOWNLOAD", "UPLOAD", "WEIGHT", "ADDRESS")}
	start := max(0, m.cursor-3)
	end := min(len(links), start+6)
	for i := start; i < end; i++ {
		d := links[i]
		if !d.Physical && !policy {
			continue
		}
		cursor := " "
		if i == m.cursor {
			cursor = ">"
		}
		selected := " "
		if chosen(cfg, d) {
			selected = "*"
		}
		health := d.Connectivity
		if health == "" || health == "unknown" {
			health = d.State
		}
		if !d.Connected {
			health = "offline"
		}
		if d.Metered && !cfg.AllowMetered {
			health = "metered"
		}
		rows = append(rows, fmt.Sprintf("%s%s %-15s %-10s %12s %12s %6d  %s", cursor, selected, safe(d.Name), safe(health), rate(d.RXRate), rate(d.TXRate), linkWeight(cfg, d.Name), safe(d.Address)))
	}
	if len(rows) == 1 {
		rows = append(rows, "  No interfaces reported yet")
	}
	if len(links) > 6 {
		rows = append(rows, "  j/k or arrows to scroll interfaces")
	}
	return box("UPLINKS  * selected", rows, width)
}
func (m *model) overview(width, height int) []string {
	s := m.snapshot
	down, up := []float64{}, []float64{}
	for _, sample := range s.History {
		down = append(down, sample.RX)
		up = append(up, sample.TX)
	}
	half := (width - 1) / 2
	rx := box("↓ DOWNLOAD", []string{" " + rate(s.RXRate), " " + spark(down, half-4), " Total  " + volume(s.RXBytes)}, half)
	tx := box("↑ UPLOAD", []string{" " + rate(s.TXRate), " " + spark(up, width-half-5), " Total  " + volume(s.TXBytes)}, width-half-1)
	rows := []string{}
	for i := range rx {
		rows = append(rows, rx[i]+" "+tx[i])
	}
	rows = append(rows, m.interfaces(width, false)...)
	if s.ActiveMode == "relay" {
		rows = append(rows, fit(fmt.Sprintf(" Relay active %d | MPTCP %d | TCP fallback %d | failures %d | peak subflows %d", s.Relay.Active, s.Relay.MPTCP, s.Relay.TCPFallback, s.Relay.Failures, s.Relay.MaxSubflows), width))
	}
	if links := m.uplinks(); len(links) > 0 {
		d := links[m.cursor]
		rows = append(rows, box("PATH DETAIL", []string{fmt.Sprintf(" %s   gateway %s   MTU %d   %s", safe(d.Kind), safe(d.Gateway), d.MTU, safe(d.State)), fmt.Sprintf(" Errors RX/TX %d/%d   Drops %d/%d   Metered %t", d.RXErrors, d.TXErrors, d.RXDropped, d.TXDropped, d.Metered)}, width)...)
	}
	remaining := height - len(rows) - 2
	if remaining > 2 {
		rows = append(rows, box("RECENT ACTIVITY", m.eventRows(max(1, remaining-2)), width)...)
	}
	return rows
}
func (m *model) eventRows(count int) []string {
	events := append([]service.Event{}, m.snapshot.Events...)
	events = append(events, m.journal...)
	sort.SliceStable(events, func(i, j int) bool { return events[i].Time.Before(events[j].Time) })
	end := len(events) - min(m.scroll, max(0, len(events)-count))
	start := max(0, end-count)
	rows := []string{}
	for _, e := range events[start:end] {
		rows = append(rows, fmt.Sprintf(" %s %-7s %-16s %s", e.Time.Format("15:04:05"), safe(e.Level), safe(e.Source), safe(e.Message)))
	}
	if len(rows) == 0 {
		rows = append(rows, " No activity yet. Press l to load the system journal.")
	}
	return rows
}
func (m *model) reviewRows(width int) []string {
	p := m.plan
	rows := []string{" Mode: " + safe(p.Mode) + "   Uplinks: " + safe(strings.Join(p.Interfaces, ", "))}
	for _, warning := range p.Warnings {
		rows = append(rows, wrap(" ! "+safe(warning), width-2)...)
	}
	rows = append(rows, "", " Commands and firewall rules:")
	for _, c := range p.Commands {
		rows = append(rows, wrap(" "+safe(c.Program+" "+strings.Join(c.Args, " ")), width-2)...)
	}
	for _, line := range strings.Split(p.NFT, "\n") {
		rows = append(rows, wrap(" "+safe(line), width-2)...)
	}
	return rows
}
func (m *model) render(width, height int, color bool) string {
	width = max(20, min(width, 240))
	height = max(5, height)
	rows := []string{fit(" VEAVER  /  NetworkManager console", width)}
	s := m.snapshot
	stamp := "waiting"
	if !s.Time.IsZero() {
		stamp = s.Time.Format("15:04:05")
		if time.Since(s.Time) > 8*time.Second {
			stamp += " STALE"
		}
	}
	rows = append(rows, fit(fmt.Sprintf(" Active %-8s | NM %-12s | RX %s  TX %s | %s", safe(s.ActiveMode), safe(s.Network.State), rate(s.RXRate), rate(s.TXRate), stamp), width))
	tabs := []string{"1 Overview", "2 Diagnostics", "3 Logs", "4 Policy"}
	tabs[m.page] = "[" + tabs[m.page] + "]"
	rows = append(rows, fit(" "+strings.Join(tabs, "  "), width))
	available := height - len(rows) - 3
	body := []string{}
	if width < 72 || height < 20 {
		body = box("COMPACT", []string{" Resize to 72x20 for the full console", " Download " + rate(s.RXRate), " Upload   " + rate(s.TXRate), " Mode     " + safe(s.ActiveMode)}, width)
	} else if m.plan != nil {
		all := m.reviewRows(width)
		start := min(m.scroll, max(0, len(all)-max(1, available-2)))
		body = box("REVIEW  Enter apply / Esc cancel / j,k scroll", all[start:min(len(all), start+max(1, available-2))], width)
	} else {
		switch m.page {
		case 0:
			body = m.overview(width, available)
		case 1:
			checks := []string{}
			for _, c := range s.Checks {
				checks = append(checks, wrap(fmt.Sprintf(" %-7s %s: %s", safe(c.Status), safe(c.Name), safe(c.Detail)), width-2)...)
			}
			start := min(m.scroll, max(0, len(checks)-max(1, available-2)))
			body = box("DIAGNOSTICS  j/k scroll", checks[start:min(len(checks), start+max(1, available-2))], width)
		case 2:
			body = box("ACTIVITY + JOURNAL  l reload / j,k scroll", m.eventRows(max(1, available-2)), width)
		case 3:
			dirty := "saved"
			if m.dirty {
				dirty = "unsaved draft"
			}
			body = box("POLICY  "+dirty, []string{fmt.Sprintf(" Mode %s    Include metered %t    Selection %s", safe(m.draft.Mode), m.draft.AllowMetered, map[bool]string{true: "automatic", false: "explicit"}[len(m.draft.Interfaces) == 0]), " o Observe / b Balance / r Relay / m Metered / u Auto select", " Space select link / +/- weight / x discard / a review", " Weights affect new TCP/UDP connections; active flows stay pinned.", " Relay: " + safe(m.draft.Relay.Address) + "  (configure credentials with --file)"}, width)
			body = append(body, m.interfaces(width, true)...)
		}
	}
	for _, row := range body[:min(len(body), max(0, available))] {
		rows = append(rows, fit(row, width))
	}
	for len(rows) < height-3 {
		rows = append(rows, strings.Repeat(" ", width))
	}
	errorText := ""
	if s.LastError != "" {
		errorText = "Policy: " + safe(s.LastError)
	}
	rows = append(rows, fit(errorText, width))
	note := safe(m.note)
	if m.busy {
		note = "Working... " + note
	}
	rows = append(rows, fit(" "+note, width), fit(" q quit  Tab/1-4 views  j/k move  f refresh  l journal  |  IPv4 host traffic", width))
	var out strings.Builder
	for i, row := range rows[:min(height, len(rows))] {
		if color {
			if i == 0 {
				out.WriteString("\x1b[1;36m")
			} else if strings.HasPrefix(row, "╭") || strings.HasPrefix(row, "╰") {
				out.WriteString("\x1b[36m")
			} else {
				out.WriteString("\x1b[37m")
			}
		}
		out.WriteString(row)
		if color {
			out.WriteString("\x1b[0m\x1b[K")
		}
		if i < len(rows)-1 {
			if color {
				out.WriteString("\r\n")
			} else {
				out.WriteByte('\n')
			}
		}
	}
	if !color {
		out.WriteByte('\n')
	}
	return out.String()
}
