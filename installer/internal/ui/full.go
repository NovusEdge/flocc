package ui

import (
	"fmt"
	"io"
	"os/exec"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
)

const (
	railWidth   = 22
	railSep     = " │ "
	paneTail    = 6
	minFullCols = 80
	minFullRows = 20
	hints       = "l log · ? keys · q quit"
	hintsLong   = "l log · ↑↓ PgUp PgDn scroll · q quit"
)

type fullStep struct {
	ev    Event // the latest event of the step; Name is always set
	start time.Time
	lines []string
}

func (s *fullStep) running() bool { return s.ev.State == Running }

// paneItem is either a step (rows are drawn from its latest event) or a block of text.
type paneItem struct {
	step *fullStep
	text string
}

type fullGroup struct {
	name  string
	items []paneItem
}

func (g *fullGroup) steps() (running, ended int, failed, pending bool) {
	for _, it := range g.items {
		if it.step == nil {
			continue
		}
		switch it.step.ev.State {
		case Running:
			running++
		case Failed:
			ended++
			failed = true
		case Pending:
			ended++
			pending = true
		default:
			ended++
		}
	}
	return
}

// fullModel keeps every event it was sent: the record and a hand-over replay them.
type fullModel struct {
	events      []Event
	groups      []*fullGroup
	cur         int // index into groups, -1 before the first
	steps       map[string]*fullStep
	plan        []string
	todos       int
	log         string
	title       string
	version     string
	start       time.Time
	now         func() time.Time
	width       int
	height      int
	handover    bool
	logView     bool
	scroll      int // lines above the tail in the log view
	showKeys    bool
	spinner     spinner.Model
	bar         progress.Model
	form        *huh.Form
	result      func() any
	reply       chan promptReply
	interrupted bool
	interrupt   func()
}

func newFullModel(now func() time.Time, version string) fullModel {
	return fullModel{
		cur:       -1,
		steps:     map[string]*fullStep{},
		version:   version,
		start:     now(),
		now:       now,
		spinner:   spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(lipgloss.NewStyle().Foreground(lipgloss.Yellow))),
		bar:       progress.New(),
		interrupt: interruptSelf,
	}
}

// buildVersion is the main module's version, which is "(devel)" or empty outside a tagged build.
func buildVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := strings.TrimPrefix(bi.Main.Version, "v"); v != "" && v != "(devel)" {
			return v
		}
	}
	return "dev"
}

func (m fullModel) Init() tea.Cmd { return m.spinner.Tick }

func (m fullModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case drawMsg:
		m.add(msg.e)
		return m, nil
	case promptMsg:
		m.form, m.result = formFor(msg.req)
		m.reply = msg.reply
		return m, m.form.Init()
	case handoffMsg:
		return m, tea.ExecProcess(msg.cmd, func(err error) tea.Msg { return handoffDone{err, msg.reply} })
	case handoffDone:
		msg.reply <- msg.err
		return m, nil
	case closeMsg:
		return m, tea.Sequence(tea.Quit)
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.handover = msg.Width < minFullCols || msg.Height < minFullRows
	case tea.KeyPressMsg:
		if m.form == nil {
			return m.key(msg.String())
		}
	}
	return m.toForm(msg)
}

func (m fullModel) key(k string) (tea.Model, tea.Cmd) {
	page := max(m.bodyHeight()-1, 1)
	switch k {
	case "q", "ctrl+c":
		m.interrupted = true
		m.interrupt()
	case "l":
		m.logView, m.scroll = !m.logView, 0
	case "esc":
		m.logView, m.scroll = false, 0
	case "?":
		m.showKeys = !m.showKeys
	case "up":
		m.scrollBy(1)
	case "down":
		m.scrollBy(-1)
	case "pgup":
		m.scrollBy(page)
	case "pgdown":
		m.scrollBy(-page)
	}
	return m, nil
}

func (m *fullModel) scrollBy(n int) {
	if !m.logView {
		return
	}
	limit := 0
	if s := m.selected(); s != nil {
		limit = max(len(s.lines)-(m.bodyHeight()-1), 0)
	}
	m.scroll = min(max(m.scroll+n, 0), limit)
}

func (m *fullModel) group(name string) {
	for i, g := range m.groups {
		if g.name == name {
			m.cur = i
			return
		}
	}
	m.groups = append(m.groups, &fullGroup{name: name})
	m.cur = len(m.groups) - 1
}

func (m *fullModel) add(e Event) {
	m.events = append(m.events, e)
	switch e.Kind {
	case KGroup:
		m.group(e.Name)
	case KPlan:
		m.plan = strings.Split(e.Text, "\n")
	case KLog:
		m.log = e.Text
	case KTodo:
		m.todos++
	case KNote:
		m.text(NoteText(e.Name, e.Text))
	case KTable:
		if e.Table != nil {
			m.text(TableText(*e.Table))
		}
	case KLine:
		if s := m.steps[e.ID]; s != nil {
			s.lines = append(s.lines, strings.TrimRight(e.Text, "\r\n"))
		}
	case KStep:
		s := m.steps[e.ID]
		if s == nil {
			s = &fullStep{start: m.now()}
			m.steps[e.ID] = s
			m.ensureGroup()
			g := m.groups[m.cur]
			g.items = append(g.items, paneItem{step: s})
		}
		if e.Detail == "" && e.State == Running {
			e.Detail = s.ev.Detail
		}
		if e.Total == 0 && e.State == Running {
			e.N, e.Total = s.ev.N, s.ev.Total
		}
		s.ev = e
	}
}

// ensureGroup gives steps and notes that arrive before any group a section of their own.
func (m *fullModel) ensureGroup() {
	if m.cur < 0 {
		m.group("Steps")
	}
}

func (m *fullModel) text(t string) {
	m.ensureGroup()
	g := m.groups[m.cur]
	g.items = append(g.items, paneItem{text: t})
}

// selected is the step the pane tails: the newest running step of the current group, else its
// newest step.
func (m fullModel) selected() *fullStep {
	if m.cur < 0 {
		return nil
	}
	var last *fullStep
	var running *fullStep
	for _, it := range m.groups[m.cur].items {
		if it.step == nil {
			continue
		}
		last = it.step
		if it.step.running() {
			running = it.step
		}
	}
	if running != nil {
		return running
	}
	return last
}

// railNames is the plan in order, then any group the plan did not name.
func (m fullModel) railNames() []string {
	names := slices.Clone(m.plan)
	for _, g := range m.groups {
		if !slices.Contains(names, g.name) {
			names = append(names, g.name)
		}
	}
	return names
}

func (m fullModel) find(name string) *fullGroup {
	for _, g := range m.groups {
		if g.name == name {
			return g
		}
	}
	return nil
}

// fraction is the share of groups that ended every step they started.
func (m fullModel) fraction() float64 {
	total := len(m.plan)
	if total == 0 {
		total = len(m.groups)
	}
	if total == 0 {
		return 0
	}
	done := 0
	for _, g := range m.groups {
		if running, ended, _, _ := g.steps(); running == 0 && ended > 0 {
			done++
		}
	}
	return min(float64(done)/float64(total), 1)
}

func (m fullModel) dims() (w, h int) {
	w, h = m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	return
}

// bodyHeight is what is left between the header rule and the bottom rule.
func (m fullModel) bodyHeight() int {
	_, h := m.dims()
	return max(h-4, 1)
}

func (m fullModel) railLine(name string) string {
	g := m.find(name)
	if g == nil {
		return Mark(Skipped, true) + " " + name
	}
	running, ended, failed, pending := g.steps()
	started := 0
	for _, it := range g.items {
		if it.step != nil {
			started++
		}
	}
	mark := Mark(Done, true)
	switch {
	case running > 0:
		mark = m.spinner.View()
	case failed:
		mark = Mark(Failed, true)
	case pending:
		mark = Mark(Pending, true)
	case ended == 0:
		mark = Mark(Skipped, true)
	}
	count := fmt.Sprintf("%d/%d", ended, started)
	room := max(railWidth-2-len(count)-1, 1)
	label := lipgloss.NewStyle().MaxWidth(room).Render(name)
	gap := strings.Repeat(" ", max(railWidth-2-lipgloss.Width(label)-len(count), 1))
	return mark + " " + label + gap + count
}

func (m fullModel) rail() []string {
	var lines []string
	for _, n := range m.railNames() {
		lines = append(lines, m.railLine(n))
	}
	if m.todos > 0 {
		lines = append(lines, "", Mark(Pending, true)+fmt.Sprintf(" %d left for you", m.todos))
	}
	return lines
}

func (m fullModel) paneLines(width int) []string {
	out := Out{Color: true}
	if m.form != nil {
		return strings.Split(m.form.View(), "\n")
	}
	if m.logView {
		return m.logLines(width)
	}
	if m.cur < 0 {
		return nil
	}
	g := m.groups[m.cur]
	lines := []string{out.Bold(g.name)}
	for _, it := range g.items {
		switch {
		case it.step == nil:
			lines = append(lines, strings.Split(it.text, "\n")...)
		case it.step.running():
			lines = append(lines, m.runningLine(it.step, width))
		default:
			lines = append(lines, strings.Split(RowText(it.step.ev, nameWidth, true), "\n")...)
		}
	}
	if s := m.selected(); s != nil && len(s.lines) > 0 {
		lines = append(lines, "")
		for _, l := range s.lines[max(len(s.lines)-paneTail, 0):] {
			lines = append(lines, out.paint("2", indent+indent+indent+l))
		}
	}
	if room := m.bodyHeight(); len(lines) > room {
		lines = append(lines[:1], lines[len(lines)-room+1:]...)
	}
	return lines
}

func (m fullModel) runningLine(s *fullStep, width int) string {
	detail := s.ev.Detail
	if s.ev.Total > 0 {
		detail = strings.TrimSpace(fmt.Sprintf("%d/%d %s", s.ev.N, s.ev.Total, detail))
	}
	suffix := "   " + clockFmt(m.now().Sub(s.start))
	fixed := indent + "  " + fmt.Sprintf("%-*s %s", nameWidth, s.ev.Name, suffix)
	detail = cut(detail, max(width-lipgloss.Width(fixed), 1))
	head := fmt.Sprintf("%s%s %-*s %s", indent, m.spinner.View(), nameWidth, s.ev.Name, detail)
	return strings.TrimRight(head, " ") + suffix
}

func (m fullModel) logLines(width int) []string {
	s := m.selected()
	if s == nil {
		return []string{Out{Color: true}.Bold("log"), "no step yet"}
	}
	room := max(m.bodyHeight()-1, 1)
	end := len(s.lines) - m.scroll
	lines := []string{Out{Color: true}.Bold("log of " + s.ev.Name)}
	for _, l := range s.lines[max(end-room, 0):max(end, 0)] {
		lines = append(lines, cut(l, width))
	}
	return lines
}

// cell clips s to w columns and pads it to w.
func cell(s string, w int) string {
	s = lipgloss.NewStyle().MaxWidth(w).Render(s)
	return s + strings.Repeat(" ", max(w-lipgloss.Width(s), 0))
}

func (m fullModel) View() tea.View {
	w, _ := m.dims()
	out := Out{Color: true}
	dim := func(s string) string { return out.paint("2", s) }

	right := "v" + m.version + " · " + clockFmt(m.now().Sub(m.start))
	title := lipgloss.NewStyle().MaxWidth(max(w-lipgloss.Width(right)-1, 1)).Render(m.title)
	gap := strings.Repeat(" ", max(w-lipgloss.Width(title)-lipgloss.Width(right), 1))
	header := out.paint("1;36", title) + gap + dim(right)
	rule := dim(strings.Repeat("─", w))

	bh := m.bodyHeight()
	paneW := max(w-railWidth-lipgloss.Width(railSep), 1)
	rail, pane := m.rail(), m.paneLines(paneW)
	lines := []string{header, rule}
	for i := range bh {
		var l, p string
		if i < len(rail) {
			l = rail[i]
		}
		if i < len(pane) {
			p = pane[i]
		}
		row := cell(l, railWidth) + dim(railSep) + lipgloss.NewStyle().MaxWidth(paneW).Render(p)
		lines = append(lines, strings.TrimRight(row, " "))
	}
	keys := hints
	if m.showKeys {
		keys = hintsLong
	}
	bar := m.bar
	bar.SetWidth(max(min(40, w-lipgloss.Width(keys)-8), 10))
	lines = append(lines, rule, bar.ViewAs(m.fraction())+"  "+dim(keys))
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	return v
}

// toForm gives the open form every message it was not handed already, and closes it when it ends.
func (m fullModel) toForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.form == nil {
		return m, nil
	}
	_, cmd := m.form.Update(msg)
	switch m.form.State {
	case huh.StateCompleted:
		m.reply <- promptReply{v: m.result()}
	case huh.StateAborted:
		m.reply <- promptReply{err: huh.ErrUserAborted}
	default:
		return m, cmd
	}
	m.form, m.result, m.reply = nil, nil, nil
	return m, cmd
}

type full struct {
	p     *tea.Program
	out   io.Writer
	done  chan struct{}
	final fullModel // set before done closes
}

// NewFull draws the session on the alternate screen of out, reading keys from in. Prompts and
// hand-overs use the inline mechanism until the full-screen ones exist.
func NewFull(out io.Writer, in io.Reader, title string) Renderer { return newFull(out, in, title) }

func newFull(out io.Writer, in io.Reader, title string, opts ...tea.ProgramOption) *full {
	r := &full{out: out, done: make(chan struct{})}
	m := newFullModel(time.Now, buildVersion())
	m.title = title
	opts = append([]tea.ProgramOption{tea.WithOutput(out), tea.WithInput(in), tea.WithoutSignalHandler()}, opts...)
	r.p = tea.NewProgram(m, opts...)
	go func() {
		defer close(r.done)
		if fm, err := r.p.Run(); err == nil {
			r.final, _ = fm.(fullModel)
		}
	}()
	return r
}

func (r *full) Draw(e Event) { r.p.Send(drawMsg{e}) }

func (r *full) Prompt(p PromptReq) (any, error) {
	reply := make(chan promptReply, 1)
	r.p.Send(promptMsg{req: p, reply: reply})
	select {
	case a := <-reply:
		return a.v, a.err
	case <-r.done:
		return nil, errInlineEnded
	}
}

func (r *full) Handoff(why string, c *exec.Cmd) error {
	reply := make(chan error, 1)
	r.p.Send(handoffMsg{why: why, cmd: c, reply: reply})
	select {
	case err := <-reply:
		return err
	case <-r.done:
		return errInlineEnded
	}
}

// Close leaves the alternate screen, so nothing drawn there survives; the to-dos and the log path
// go to the normal screen as text.
func (r *full) Close(todos []string) int {
	r.p.Send(closeMsg{todos})
	<-r.done
	if text := CloseText(todos, r.final.log); text != "" {
		io.WriteString(r.out, text)
	}
	return 0
}
