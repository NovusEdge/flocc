package dash

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/NovusEdge/flocc/installer/internal/safe"
	"github.com/NovusEdge/flocc/installer/internal/ship"
	"github.com/NovusEdge/flocc/installer/internal/ui/theme"
)

// focus is the pane the fleet screen's arrows move in; the zero value is the issues pane.
type focus int

const (
	issuesPane focus = iota
	hostsPane
)

const (
	activityN    = 5
	minIssueRows = 3 // content rows the issues pane keeps before the activity pane may take any
)

// visible is the claims that pass the filter, which matches key, title, host or state.
func (m *Model) visible() []row {
	if m.filter == "" {
		return m.rows
	}
	f := strings.ToLower(m.filter)
	var out []row
	for _, r := range m.rows {
		c := r.claim
		for _, s := range []string{c.Issue, m.titles[c.Issue].Title, r.host, c.State} {
			if strings.Contains(strings.ToLower(s), f) {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

func stateGlyph(th theme.Theme, spin string, state string) string {
	switch state {
	case "running":
		return spin
	case "blocked":
		return th.Err.Render(theme.FailMark)
	case "done":
		return th.Up.Render(theme.DoneMark)
	}
	return th.Dim.Render("·")
}

// row draws s behind the cursor. A selected row is drawn without the colours inside s, which
// the cursor's own style would end at the first reset.
func (m *Model) row(sel bool, s string) string {
	if sel {
		s = ansi.Strip(s)
	}
	return m.th.Row(sel, s)
}

func (m *Model) sev(s string, v ship.Sev) string {
	switch v {
	case ship.SevDim:
		return m.th.Dim.Render(s)
	case ship.SevWarn:
		return m.th.Warn.Render(s)
	case ship.SevBad:
		return m.th.Err.Render(s)
	}
	return s
}

func (m *Model) fleetView() string {
	iw, _ := theme.Inner(m.w, m.h)
	bh := m.bodyHeight()
	hostLines, selAt := m.hostLines()
	needH := len(hostLines) + 2
	capTop := max(bh/2, 3)
	topH := min(needH, capTop)
	top := m.th.Pane("hosts", "", strings.Join(theme.Window(hostLines, selAt, topH-2), "\n"), iw, topH)

	avail := bh - topH
	act, actKey := m.activityLines()
	actH := min(len(act)+2, avail-(minIssueRows+2))
	if actH < 3 {
		actH = 0
	}
	vis := m.visible()
	at := min(m.sel+1, len(vis))
	hint := fmt.Sprintf("%d of %d%s/ filter", at, len(vis), theme.Sep)
	if m.filter != "" {
		hint = fmt.Sprintf("%d of %d%sfilter: %s", at, len(vis), theme.Sep, safe.Sanitize(m.filter))
	}
	issuesH := avail - actH
	out := []string{top, m.th.Pane("issues", hint, strings.Join(m.issueLines(iw-4, issuesH-2), "\n"), iw, issuesH)}
	if actH > 0 {
		out = append(out, m.th.Pane("activity"+theme.Sep+actKey, "", strings.Join(act, "\n"), iw, actH))
	}
	return strings.Join(out, "\n")
}

// facts is a host's second row: tick, live sessions over the cap, usage and disk.
func (m *Model) facts(rep *ship.Report) string {
	live := 0
	for _, c := range rep.Claims {
		if c.TmuxAlive {
			live++
		}
	}
	tick := func() string { s, v := ship.TickFact(rep); return m.sev(s, v) }
	pct := func(label string, v, limit float64) string {
		s, sv := ship.PctFact(v, limit)
		return label + " " + m.sev(s, sv)
	}
	five, week := "5h "+m.sev("-", ship.SevDim), "wk "+m.sev("-", ship.SevDim)
	if u := rep.Usage; u != nil {
		five, week = pct("5h", u.FiveHourPct, rep.FiveHourMaxPct), pct("wk", u.WeeklyPct, rep.WeeklyMaxPct)
	}
	disk, dv := ship.DiskFact(rep)
	return strings.Join([]string{"tick " + tick(), fmt.Sprintf("%d/%d", live, rep.SessionCap), five, week, m.sev(disk, dv)}, theme.Sep)
}

// hostLines are one or two rows per host, and the row of the selected host's first line.
func (m *Model) hostLines() (out []string, selAt int) {
	th := m.th
	if len(m.hosts) == 0 {
		return []string{th.Dim.Render("no hosts yet, run flocc setup")}, 0
	}
	for i, h := range m.hosts {
		if i == m.hostSel {
			selAt = len(out)
		}
		name := safe.Sanitize(h.host.Name)
		var first string
		switch {
		case h.rep == nil && h.host.State != "" && h.host.State != "started":
			first = th.Down.Render(theme.DownMark) + " " + th.Dim.Render(name+"  "+h.err)
		case h.err != "":
			first = th.Down.Render(theme.DownMark) + " " + th.Dim.Render(name) + "  " + th.Err.Render(h.err)
		default:
			ver := th.Warn.Render("behind" + theme.Sep + "ship to update")
			if h.rep != nil && h.rep.V > 0 {
				ver = fmt.Sprintf("v%d", h.rep.V)
			}
			first = th.Up.Render(theme.UpMark) + " " + name + "  " + ver
		}
		out = append(out, m.row(m.focus == hostsPane && i == m.hostSel, first))
		if h.rep != nil {
			second := m.facts(h.rep)
			if h.err != "" {
				second = th.Dim.Render(ansi.Strip(second))
			}
			out = append(out, "    "+second)
		}
	}
	return out, selAt
}

// issueCells are the columns right of the title.
type issueCells struct{ phase, ctx, host string }

func cells(r row) issueCells {
	c := r.claim
	phase := safe.Sanitize(c.Phase)
	if c.State != "running" {
		phase += theme.Sep + safe.Sanitize(c.State)
	} else if phase != "" {
		phase += theme.Sep
	}
	if c.UpdatedAge != nil {
		age := ship.Age(*c.UpdatedAge)
		if *c.UpdatedAge < 5 {
			age = "now"
		}
		phase = strings.TrimRight(phase, " ") + " " + age
	}
	ctx := "ctx -"
	if c.ContextPct != nil {
		ctx = fmt.Sprintf("ctx %.0f%%", *c.ContextPct)
	}
	return issueCells{strings.TrimLeft(strings.TrimSuffix(phase, theme.Sep), " "), ctx, safe.Sanitize(r.host)}
}

func pad(s string, w int) string { return s + strings.Repeat(" ", max(w-lipgloss.Width(s), 0)) }

// issueLines draws the visible claims that fit in room rows, the window following the selection.
func (m *Model) issueLines(inner, room int) []string {
	vis := m.visible()
	if len(vis) == 0 {
		msg := "no claimed issues"
		if m.filter != "" {
			msg = "no issue matches, esc clears"
		}
		return []string{m.th.Dim.Render(msg)}
	}
	room = max(room, 1)
	first := max(min(m.sel-room/2, len(vis)-room), 0)
	window := vis[first:min(first+room, len(vis))]

	keyW, phaseW, ctxW, hostW := 8, 0, 0, 0
	cs := make([]issueCells, len(window))
	for i, r := range window {
		cs[i] = cells(r)
		keyW = max(keyW, lipgloss.Width(safe.Sanitize(r.claim.Issue)))
		phaseW = max(phaseW, lipgloss.Width(cs[i].phase))
		ctxW = max(ctxW, lipgloss.Width(cs[i].ctx))
		hostW = max(hostW, lipgloss.Width(cs[i].host))
	}
	phaseW, hostW = min(phaseW, 24), min(hostW, 16)
	bodyW := inner - 2
	leftW := 1 + 1 + keyW + 2

	var out []string
	for i, r := range window {
		c, x := r.claim, cs[i]
		right := pad(x.phase, phaseW) + "  " + pad(x.ctx, ctxW) + "  " + pad(x.host, hostW)
		if bodyW-leftW-lipgloss.Width(right)-2 < 12 {
			right = pad(x.phase, phaseW) + "  " + x.ctx
		}
		if bodyW-leftW-lipgloss.Width(right)-2 < 12 {
			right = x.phase
		}
		tw := max(bodyW-leftW-lipgloss.Width(right)-2, 0)
		title := ansi.Truncate(m.titles[c.Issue].Title, tw, "…")
		line := stateGlyph(m.th, m.spin.View(), c.State) + " " + pad(safe.Sanitize(c.Issue), keyW) + "  " + pad(title, tw) + "  " + right
		if r.stale {
			line = m.th.Dim.Render(ansi.Strip(line))
		}
		out = append(out, m.row(m.focus == issuesPane && first+i == m.sel, line))
	}
	return out
}

// activityLines are the selected issue's last activity, and its key for the pane title.
func (m *Model) activityLines() ([]string, string) {
	r, ok := m.selected()
	if !ok {
		return []string{m.th.Dim.Render("nothing selected")}, ""
	}
	key := safe.Sanitize(r.claim.Issue)
	if len(r.claim.Activity) == 0 {
		return []string{m.th.Dim.Render("no activity reported")}, key
	}
	var out []string
	for _, a := range r.claim.Activity[max(len(r.claim.Activity)-activityN, 0):] {
		out = append(out, m.th.Dim.Render(time.Unix(int64(a.At), 0).Format("15:04"))+" "+safe.Sanitize(a.Text))
	}
	return out, key
}
