package dash

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
	"github.com/NovusEdge/flocc/installer/internal/ship"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

const (
	pollEvery     = 10 * time.Second
	activityLines = 5
	hintsShort    = "↑↓ select · a attach · l logs · r release · c cardinal · ? keys · q quit"
	hintsLong     = "a opens the issue's session · l its log · r sends it back to Todo · c attaches to cardinal, read-only"
)

// Source is the fleet the dashboard polls. ship.Source is the real one.
type Source interface {
	Discover(context.Context) ([]ship.FleetHost, []string)
	Status(context.Context, ship.FleetHost) (*ship.Report, error)
	Cardinals(context.Context) ([]ship.CardinalRow, []string)
}

type Options struct {
	Source Source
	// Self is the flocc binary that attach, logs, release and cardinal attach re-run.
	Self string
	In   io.Reader
	Out  io.Writer
	// Interval defaults to pollEvery.
	Interval time.Duration
}

// hostState keeps the last good report of a host across polls that fail.
type hostState struct {
	host ship.FleetHost
	rep  *ship.Report
	err  string // of the latest poll; empty when it answered
}

// row is one claim of one host. stale marks a claim read from a report older than the latest poll.
type row struct {
	host  string
	claim ship.Claim
	stale bool
}

type fetched struct {
	results []Result
	cards   []ship.CardinalRow
	notes   []string
	at      time.Time
}

type tick time.Time

type Model struct {
	ctx  context.Context
	opts Options
	now  func() time.Time
	// command builds the child a handoff runs; a test replaces it.
	command func(args ...string) *exec.Cmd

	w, h      int
	hosts     []hostState
	cards     []ship.CardinalRow
	notes     []string
	rows      []row
	sel       int
	selKey    [2]string // host and issue of the selected row, to follow it across polls
	loaded    bool
	fetching  bool
	startedAt time.Time
	fetchedAt time.Time

	confirm  *ui.Confirm
	confirmN row
	showKeys bool
	notice   string
}

func New(ctx context.Context, o Options) *Model {
	if o.Interval <= 0 {
		o.Interval = pollEvery
	}
	m := &Model{ctx: ctx, opts: o, now: time.Now, w: 80, h: 24}
	m.command = func(args ...string) *exec.Cmd { return exec.Command(o.Self, args...) }
	return m
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.startFetch(), m.tickCmd())
}

func (m *Model) tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tick(t) })
}

func (m *Model) startFetch() tea.Cmd {
	m.fetching, m.startedAt = true, m.now()
	src, ctx := m.opts.Source, m.ctx
	return func() tea.Msg {
		var (
			cards []ship.CardinalRow
			cn    []string
			wg    sync.WaitGroup
		)
		wg.Add(1)
		go func() {
			defer wg.Done()
			cards, cn = src.Cardinals(ctx)
		}()
		hs, notes := src.Discover(ctx)
		res := Fetch(ctx, hs, src.Status)
		wg.Wait()
		return fetched{results: res, cards: cards, notes: append(notes, cn...), at: time.Now()}
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil
	case fetched:
		if msg.at.Before(m.fetchedAt) {
			return m, nil
		}
		m.apply(msg)
		return m, nil
	case tick:
		cmd := m.tickCmd()
		if !m.fetching && m.now().Sub(m.startedAt) >= m.opts.Interval {
			cmd = tea.Batch(cmd, m.startFetch())
		}
		return m, cmd
	case execDone:
		m.notice = ""
		if msg.err != nil {
			m.notice = fmt.Sprintf("%s: %v", msg.what, msg.err)
		}
		return m, m.startFetch()
	case tea.KeyPressMsg:
		if m.confirm == nil {
			return m.key(msg)
		}
	}
	return m.toConfirm(msg)
}

// apply folds a poll into the state. A host that failed keeps its last report, which its rows
// show as stale.
func (m *Model) apply(f fetched) {
	prev := map[string]*ship.Report{}
	for _, h := range m.hosts {
		prev[h.host.Name] = h.rep
	}
	m.hosts = m.hosts[:0]
	for _, r := range f.results {
		st := hostState{host: r.Host, rep: r.Status}
		if r.Err != nil {
			st.err = saved.Sanitize(r.Err.Error())
			st.rep = prev[r.Host.Name]
		}
		m.hosts = append(m.hosts, st)
	}
	slices.SortFunc(m.hosts, func(a, b hostState) int { return strings.Compare(a.host.Name, b.host.Name) })
	m.cards, m.notes = f.cards, f.notes
	m.loaded, m.fetching, m.fetchedAt = true, false, f.at
	m.rebuild()
}

func (m *Model) rebuild() {
	m.rows = m.rows[:0]
	for _, h := range m.hosts {
		if h.rep == nil {
			continue
		}
		claims := slices.Clone(h.rep.Claims)
		slices.SortFunc(claims, func(a, b ship.Claim) int { return strings.Compare(a.Issue, b.Issue) })
		for _, c := range claims {
			m.rows = append(m.rows, row{host: h.host.Name, claim: c, stale: h.err != ""})
		}
	}
	if i := slices.IndexFunc(m.rows, func(r row) bool { return [2]string{r.host, r.claim.Issue} == m.selKey }); i >= 0 {
		m.sel = i
	}
	m.sel = max(min(m.sel, len(m.rows)-1), 0)
	m.remember()
}

func (m *Model) remember() {
	m.selKey = [2]string{}
	if m.sel < len(m.rows) {
		m.selKey = [2]string{m.rows[m.sel].host, m.rows[m.sel].claim.Issue}
	}
}

func (m *Model) selected() (row, bool) {
	if m.sel < 0 || m.sel >= len(m.rows) {
		return row{}, false
	}
	return m.rows[m.sel], true
}

func (m *Model) toConfirm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.confirm == nil {
		return m, nil
	}
	cmd, done, yes := m.confirm.Update(msg)
	if !done {
		return m, cmd
	}
	r := m.confirmN
	m.confirm = nil
	if !yes {
		return m, cmd
	}
	return m, tea.Batch(cmd, m.handoff("release "+r.claim.Issue, "-H", r.host, "release", r.claim.Issue))
}

var (
	green  = lipgloss.NewStyle().Foreground(lipgloss.Green)
	red    = lipgloss.NewStyle().Foreground(lipgloss.Red)
	yellow = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
	faint  = lipgloss.NewStyle().Faint(true)
	bold   = lipgloss.NewStyle().Bold(true)
)

func (m *Model) View() tea.View {
	frame := m.frame()
	if m.confirm != nil {
		frame = m.confirm.Over(frame, m.w, m.h)
	}
	v := tea.NewView(frame)
	v.AltScreen = true
	return v
}

func (m *Model) frame() string {
	rule := faint.Render(strings.Repeat("─", m.w))
	strip := m.strip()
	activity := m.activity()
	// header and rule above the strip, then rule, column titles, rows, rule, activity, rule, hints.
	room := max(m.h-(2+len(strip)+2+1+len(activity)+1+1), 1)

	lines := append([]string{m.header(), rule}, strip...)
	lines = append(lines, rule)
	lines = append(lines, m.table(room)...)
	lines = append(lines, rule)
	lines = append(lines, activity...)
	lines = append(lines, rule, m.hints())
	for i, l := range lines {
		lines[i] = lipgloss.NewStyle().MaxWidth(m.w).Render(l)
	}
	return strings.Join(lines, "\n")
}

func (m *Model) header() string {
	left := fmt.Sprintf(" %s  %d hosts · %d issues", bold.Render("flocc"), len(m.hosts), len(m.rows))
	if dot, ok := m.cardinalDot(); ok {
		left += " · cardinal " + dot
	}
	right := "loading"
	if m.loaded {
		right = "refreshed " + ship.Age(max(m.now().Sub(m.fetchedAt).Seconds(), 0)) + " ago"
	}
	gap := max(m.w-lipgloss.Width(left)-lipgloss.Width(right)-1, 1)
	return left + strings.Repeat(" ", gap) + faint.Render(right)
}

// cardinalDot is green when every saved install is healthy and red otherwise; ok is false with no
// installs.
func (m *Model) cardinalDot() (string, bool) {
	if len(m.cards) == 0 {
		return "", false
	}
	for _, c := range m.cards {
		if c.Status == nil || len(c.Status.Failures()) > 0 {
			return red.Render("●"), true
		}
	}
	return green.Render("●"), true
}

func (m *Model) strip() []string {
	if len(m.hosts) == 0 {
		return []string{faint.Render("  no hosts")}
	}
	nameW := 0
	for _, h := range m.hosts {
		nameW = max(nameW, lipgloss.Width(saved.Sanitize(h.host.Name)))
	}
	out := make([]string, 0, len(m.hosts))
	for _, h := range m.hosts {
		name := fmt.Sprintf("%-*s", nameW, saved.Sanitize(h.host.Name))
		dot, text := green.Render("●"), ""
		switch {
		case h.err != "":
			dot, text = red.Render("●"), red.Render(h.err)
			if h.rep != nil {
				text += faint.Render(" (stale)")
			}
		case h.rep != nil:
			text = hostColumns(h.rep)
		}
		out = append(out, "  "+dot+" "+name+"  "+text)
	}
	return out
}

func hostColumns(r *ship.Report) string {
	ver := fmt.Sprintf("v%d", r.V)
	if r.V == 0 {
		ver = yellow.Render("behind, ship to update")
	}
	tick := "tick -"
	if r.LastTick != nil && r.LastTick.AgeS != nil {
		tick = "tick " + ship.Age(*r.LastTick.AgeS) + " ago"
	}
	usage := "5h -  wk -"
	if r.Usage != nil {
		usage = fmt.Sprintf("5h %2.0f%%  wk %2.0f%%", r.Usage.FiveHourPct, r.Usage.WeeklyPct)
	}
	disk := "disk -"
	if r.Disk != nil && r.Disk.TotalBytes > 0 {
		disk = fmt.Sprintf("disk %.0f GiB", float64(r.Disk.FreeBytes)/(1<<30))
	}
	return fmt.Sprintf("%s  %-14s  %s  %s  cap %d", ver, tick, usage, disk, r.SessionCap)
}

// table is the header and the rows that fit in room lines, the window following the selection.
func (m *Model) table(room int) []string {
	hostW := len("HOST")
	for _, r := range m.rows {
		hostW = max(hostW, lipgloss.Width(saved.Sanitize(r.host)))
	}
	layout := func(sel, a, b, c, d, e, f, g string) string {
		return fmt.Sprintf(" %s %-8s  %-*s  %-8s  %-8s  %-5s  %-8s  %s", sel, a, hostW, b, c, d, e, f, g)
	}
	out := []string{faint.Render(layout(" ", "ISSUE", "HOST", "PHASE", "STATE", "CTX", "LAUNCHES", "UPDATED"))}
	if len(m.rows) == 0 {
		return append(out, faint.Render("  no claimed issues"))
	}
	room = max(room-1, 1)
	first := max(min(m.sel-room/2, len(m.rows)-room), 0)
	for i := first; i < min(first+room, len(m.rows)); i++ {
		r, c := m.rows[i], m.rows[i].claim
		ctxPct, upd := "-", "-"
		if c.ContextPct != nil {
			ctxPct = fmt.Sprintf("%.0f%%", *c.ContextPct)
		}
		if c.UpdatedAge != nil {
			upd = ship.Age(*c.UpdatedAge)
			if *c.UpdatedAge < 5 {
				upd = "now"
			}
		}
		marker := " "
		if i == m.sel {
			marker = "▸"
		}
		line := layout(marker, saved.Sanitize(c.Issue), saved.Sanitize(r.host), saved.Sanitize(c.Phase), saved.Sanitize(c.State), ctxPct,
			fmt.Sprintf("%d/%d", c.Launches, c.LaunchLimit), upd)
		switch {
		case r.stale:
			line = faint.Render(line + " (stale)")
		case c.State == "blocked":
			line = yellow.Render(line)
		}
		if i == m.sel {
			line = bold.Render(line)
		}
		out = append(out, line)
	}
	return out
}

func (m *Model) activity() []string {
	r, ok := m.selected()
	if !ok {
		return []string{faint.Render("  nothing selected")}
	}
	c := r.claim
	head := fmt.Sprintf("  %s  %s", bold.Render(saved.Sanitize(c.Issue)), saved.Sanitize(c.State))
	if r.stale {
		head += faint.Render(" (stale)")
	}
	out := []string{head}
	if len(c.Activity) == 0 {
		return append(out, faint.Render("  no activity reported"))
	}
	for _, a := range c.Activity[max(len(c.Activity)-activityLines, 0):] {
		out = append(out, "  "+faint.Render(time.Unix(int64(a.At), 0).Format("15:04"))+" "+saved.Sanitize(a.Text))
	}
	return out
}

func (m *Model) hints() string {
	switch {
	case m.notice != "":
		return " " + red.Render(saved.Sanitize(m.notice))
	case m.showKeys:
		return " " + faint.Render(hintsLong)
	}
	return " " + faint.Render(hintsShort)
}

// Run opens the dashboard on o.Out and returns the exit code. It takes the terminal for itself:
// no signal handler, so ctrl+c reaches the model as a key.
func Run(ctx context.Context, o Options) int {
	p := tea.NewProgram(New(ctx, o), tea.WithInput(o.In), tea.WithOutput(o.Out), tea.WithoutSignalHandler(), tea.WithContext(ctx))
	if _, err := p.Run(); err != nil {
		if ctx.Err() != nil || errors.Is(err, tea.ErrProgramKilled) {
			return 130
		}
		fmt.Fprintln(os.Stderr, "flocc:", err)
		return 1
	}
	return 0
}
