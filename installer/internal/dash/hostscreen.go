package dash

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	kb "charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
	"github.com/NovusEdge/flocc/installer/internal/ship"
	"github.com/NovusEdge/flocc/installer/internal/ui/theme"
)

const (
	hostFactsH   = 10 // eight rows and the border
	hostFactsW   = 48
	logPaneMin   = 5 // three rows and the border
	shipToDoctor = "ship to see doctor checks"
)

// doctorMsg is one run of doctor --json. gen is Model.docGen at the time it started.
type doctorMsg struct {
	gen    int
	checks []ship.DoctorCheck
	note   string
}

// doctorCmd runs doctor on h. A host that predates doctor --json, or answers something else, gets
// a note saying to ship. The exit code is ignored: doctor exits 1 when a check fails.
func (m *Model) doctorCmd(h ship.FleetHost) tea.Cmd {
	m.docGen++
	m.docRunning, m.doctor = true, doctorMsg{}
	gen, src, ctx := m.docGen, m.opts.Source, m.ctx
	return func() tea.Msg {
		res, err := src.Run(ctx, h, []string{"doctor", "--json"})
		var rep ship.DoctorReport
		switch {
		case err != nil:
			return doctorMsg{gen: gen, note: firstLine(err.Error())}
		case oldHost(res), json.Unmarshal([]byte(res.Stdout), &rep) != nil, rep.V == 0:
			return doctorMsg{gen: gen, note: shipToDoctor}
		}
		return doctorMsg{gen: gen, checks: rep.Checks}
	}
}

// openHost moves into the host screen of the focused host.
func (m *Model) openHost() tea.Cmd {
	if m.hostSel >= len(m.hosts) {
		return nil
	}
	h := m.hosts[m.hostSel].host
	m.cur = [2]string{h.Name, ""}
	return tea.Batch(m.enter(hostScreen), m.doctorCmd(h))
}

func (m *Model) hostKey(k string) (tea.Model, tea.Cmd) {
	name := m.cur[0]
	switch k {
	case "esc", "left", "h":
		m.docGen++
		m.leave(fleetScreen)
	case "d":
		if h, ok := m.hostByName(name); ok {
			return m, m.doctorCmd(h)
		}
	case "s":
		if m.opts.ShipBlock == "" {
			return m, m.handoff("ship "+name, "ship", "-H", name)
		}
	case "l":
		return m, m.openPager()
	}
	return m, nil
}

func (m *Model) hostBar() []kb.Binding {
	t := m.th
	shipDesc := "ship"
	if m.opts.ShipBlock != "" {
		shipDesc += " (" + m.opts.ShipBlock + ")"
	}
	return []kb.Binding{
		t.Key(true, "d", "doctor"),
		t.Key(m.opts.ShipBlock == "", "s", shipDesc),
		t.Key(true, "l", "log"),
		t.Key(true, "esc", "back", "esc", "left", "h"),
		t.Key(true, "?", "keys"),
	}
}

// hostSplit is the host screen's layout: topH rows of facts and doctor above the log pane, and,
// when they are stacked, the heights of the facts pane and the doctor pane under it. The log pane
// keeps logPaneMin rows: the doctor pane shrinks first, to three rows, then the facts pane is cut;
// with less room than that the doctor pane goes (docH 0).
func (m *Model) hostSplit() (side bool, topH, factsH, docH int) {
	bh := m.bodyHeight()
	room := max(bh-logPaneMin, 3)
	need := max(len(m.doctor.checks), 1) + 2
	if m.w >= theme.StackBelow {
		return true, min(max(hostFactsH, min(need, bh-6)), room), 0, 0
	}
	factsH = min(hostFactsH, room)
	if docH = min(need, room-factsH); docH < 3 {
		// A failing check matters more than the last facts rows: cut those, down to six, to keep it.
		if room >= 6+3 {
			factsH, docH = room-3, 3
		} else {
			docH = 0
		}
	}
	return false, factsH + docH, factsH, docH
}

func (m *Model) hostView() string {
	iw, _ := theme.Inner(m.w, m.h)
	side, topH, factsH, docH := m.hostSplit()
	name := saved.Sanitize(m.cur[0])
	var rows []string
	for _, h := range m.hosts {
		if h.host.Name == m.cur[0] {
			rows = m.hostFacts(h)
		}
	}
	if rows == nil {
		rows = []string{m.th.Err.Render("no report")}
	}
	hint := "d run again"
	if m.docRunning {
		hint = "running"
	}
	doc := strings.Join(m.doctorLines(), "\n")
	var top string
	if side {
		top = lipgloss.JoinHorizontal(lipgloss.Top,
			m.th.Pane(name, "", strings.Join(rows, "\n"), hostFactsW, topH), " ",
			m.th.Pane("doctor", hint, doc, iw-hostFactsW-1, topH))
	} else {
		top = m.th.Pane(name, "", strings.Join(rows, "\n"), iw, factsH)
		if docH > 0 {
			top += "\n" + m.th.Pane("doctor", hint, doc, iw, docH)
		}
	}
	return top + "\n" + m.outputPane("log", "l full screen", iw, m.bodyHeight()-topH)
}

func (m *Model) hostFacts(h hostState) []string {
	rep := h.rep
	if rep == nil {
		return []string{m.th.Err.Render(h.err)}
	}
	const w = 9
	ver := fmt.Sprintf("status v%d", rep.V)
	if rep.V == 0 {
		ver = m.th.Warn.Render("behind" + theme.Sep + "ship to update")
	}
	tick, tv := ship.TickFact(rep)
	usage := m.sev("-", ship.SevDim)
	if u := rep.Usage; u != nil {
		pct := func(label string, v, limit float64) string {
			s, sv := ship.PctFact(v, limit)
			return fmt.Sprintf("%s %s of %.0f%%", label, m.sev(s, sv), limit)
		}
		usage = pct("5h", u.FiveHourPct, rep.FiveHourMaxPct) + theme.Sep + pct("wk", u.WeeklyPct, rep.WeeklyMaxPct)
	}
	disk, dv := ship.DiskFact(rep)
	if d := rep.Disk; d != nil && d.TotalBytes > 0 {
		disk += fmt.Sprintf(" free of %.0f GiB", float64(d.TotalBytes)/(1<<30))
	}
	login, lv := ship.LoginFact(rep)
	tokens, kv := ship.TokensFact(rep)
	rows := []string{
		m.label(w, "version", ver),
		m.label(w, "mode", saved.Sanitize(rep.Mode)),
		m.label(w, "last tick", m.sev(tick, tv)),
		m.label(w, "usage", usage),
		m.label(w, "disk", m.sev(disk, dv)),
		m.label(w, "claude", m.sev(login, lv)),
		m.label(w, "tokens", m.sev(tokens, kv)),
		m.label(w, "cap", fmt.Sprintf("%d sessions", rep.SessionCap)),
	}
	if h.err == "" {
		return rows
	}
	// The last poll failed, so the rest is its predecessor's: dimmed, with the error where the
	// version was.
	for i, r := range rows {
		rows[i] = m.th.Dim.Render(ansi.Strip(r))
	}
	rows[0] = m.th.Err.Render(h.err)
	return rows
}

func (m *Model) doctorLines() []string {
	d := m.doctor
	switch {
	case d.note != "":
		return []string{m.th.Dim.Render(d.note)}
	case m.docRunning && len(d.checks) == 0:
		return []string{m.th.Dim.Render("running")}
	case len(d.checks) == 0:
		return []string{m.th.Dim.Render("no checks")}
	}
	// Failures first: a short pane must not hide the check that needs doing something about.
	rank := func(c ship.DoctorCheck) int {
		switch {
		case !c.OK:
			return 0
		case c.Warn:
			return 1
		}
		return 2
	}
	checks := slices.Clone(d.checks)
	slices.SortStableFunc(checks, func(a, b ship.DoctorCheck) int { return rank(a) - rank(b) })
	nameW := 0
	for _, c := range checks {
		nameW = min(max(nameW, lipgloss.Width(saved.Sanitize(c.Name))), 16)
	}
	var out []string
	for _, c := range checks {
		icon := m.th.Up.Render(theme.DoneMark)
		switch {
		case !c.OK:
			icon = m.th.Err.Render(theme.FailMark)
		case c.Warn:
			icon = m.th.Warn.Render("•")
		}
		out = append(out, icon+" "+pad(saved.Sanitize(c.Name), nameW)+"  "+m.th.Dim.Render(saved.Sanitize(c.Detail)))
	}
	return out
}
