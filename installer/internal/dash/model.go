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

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/ship"
	"github.com/NovusEdge/flocc/installer/internal/ui/theme"
)

const pollEvery = 10 * time.Second

// Source is the fleet the dashboard polls. ship.Source is the real one.
type Source interface {
	Discover(context.Context) ([]ship.FleetHost, []string)
	Status(context.Context, ship.FleetHost) (*ship.Report, error)
	Cardinals(context.Context) ([]ship.CardinalRow, []string)
	Run(ctx context.Context, h ship.FleetHost, args []string) (host.Result, error)
}

// Title is an issue's Linear title and page. A zero Title is a key Linear did not return.
type Title struct{ Title, URL string }

type Options struct {
	Source Source
	// Self is the flocc binary that attach, release, ship and cardinal attach re-run.
	Self string
	In   io.Reader
	Out  io.Writer
	// Interval defaults to pollEvery.
	Interval time.Duration
	// Titles looks up Linear titles; nil, or an error, leaves the screens showing keys alone.
	Titles func(ctx context.Context, keys []string) (map[string]Title, error)
	// Open opens a URL in the browser.
	Open func(url string)
	// ShipBlock is why ship cannot run from here ("" when it can).
	ShipBlock string
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

type screen int

const (
	fleetScreen screen = iota
	issueScreen
	hostScreen
	cardinalScreen
	pagerScreen
)

// ask is the open y/N question on the status line.
type ask struct {
	text string
	yes  func() tea.Cmd
}

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

	th        theme.Theme
	screen    screen
	toasts    theme.Toasts
	ask       *ask
	spin      spinner.Model
	showKeys  bool
	titles    map[string]Title
	asked     map[string]bool // keys with a Titles call out
	titleNote string

	focus   focus
	hostSel int
	cardSel int
	filter  string
	editing editKind
}

func New(ctx context.Context, o Options) *Model {
	if o.Interval <= 0 {
		o.Interval = pollEvery
	}
	m := &Model{ctx: ctx, opts: o, now: time.Now, w: 80, h: 24, titles: map[string]Title{}, asked: map[string]bool{}}
	m.command = func(args ...string) *exec.Cmd { return exec.Command(o.Self, args...) }
	m.th = theme.FromEnv(os.Getenv, true)
	m.spin = m.th.Spinner()
	return m
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.startFetch(), m.tickCmd(), tea.RequestBackgroundColor, m.spin.Tick)
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
	case tea.BackgroundColorMsg:
		m.th = m.th.WithDark(msg.IsDark())
		m.spin.Style = m.th.Accent
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case theme.ToastExpired:
		m.toasts.Expire(msg)
		return m, nil
	case fetched:
		if msg.at.Before(m.fetchedAt) {
			return m, nil
		}
		m.apply(msg)
		return m, m.askTitles()
	case titlesMsg:
		m.gotTitles(msg)
		return m, nil
	case tick:
		cmd := m.tickCmd()
		if !m.fetching && m.now().Sub(m.startedAt) >= m.opts.Interval {
			cmd = tea.Batch(cmd, m.startFetch())
		}
		return m, cmd
	case execDone:
		cmd := m.startFetch()
		if msg.err != nil {
			cmd = tea.Batch(cmd, m.toasts.Show(msg.what+": "+saved.Sanitize(msg.err.Error()), true))
		}
		return m, cmd
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
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
	m.hostSel = max(min(m.hostSel, len(m.hosts)-1), 0)
	m.cardSel = max(min(m.cardSel, len(m.cards)-1), 0)
	m.reselect()
}

// reselect puts sel back on the remembered row among the visible ones, or keeps it in range.
func (m *Model) reselect() {
	v := m.visible()
	if i := slices.IndexFunc(v, func(r row) bool { return [2]string{r.host, r.claim.Issue} == m.selKey }); i >= 0 {
		m.sel = i
	}
	m.sel = max(min(m.sel, len(v)-1), 0)
	m.remember()
}

// remember keeps the old key when nothing is visible, so clearing a filter that matched nothing
// finds the row again.
func (m *Model) remember() {
	if v := m.visible(); m.sel < len(v) {
		m.selKey = [2]string{v[m.sel].host, v[m.sel].claim.Issue}
	}
}

func (m *Model) selected() (row, bool) {
	v := m.visible()
	if m.sel < 0 || m.sel >= len(v) {
		return row{}, false
	}
	return v[m.sel], true
}

func (m *Model) View() tea.View {
	var frame string
	if m.w < theme.MinW || m.h < theme.MinH {
		frame = m.th.TooSmall(m.w, m.h)
	} else {
		frame = m.chrome(m.body(), m.bodyHeight())
	}
	v := tea.NewView(frame)
	v.AltScreen = true
	return v
}

func (m *Model) body() string {
	return m.fleetView()
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
