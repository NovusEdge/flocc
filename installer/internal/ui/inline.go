package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
)

const tailLines = 3

type (
	drawMsg   struct{ e Event }
	promptMsg struct {
		req   PromptReq
		reply chan promptReply
	}
	promptReply struct {
		v   any
		err error
	}
	handoffMsg struct {
		why   string
		cmd   *exec.Cmd
		reply chan error
	}
	handoffDone struct {
		err   error
		reply chan error
	}
	closeMsg struct{ todos []string }
)

type liveStep struct {
	id, name, detail string
	n, total         int
	start            time.Time
	lines            []string
}

// inlineModel keeps finished rows in the terminal's scrollback (tea.Println) and redraws only the
// running steps, the tail of their output and an open form below them.
type inlineModel struct {
	steps       []liveStep
	queue       []string // rows that ended while a form was open; printed once it closes
	form        *huh.Form
	result      func() any
	reply       chan promptReply
	spinner     spinner.Model
	width       int
	now         func() time.Time
	log         string
	interrupted bool
	interrupt   func()
}

func newInlineModel(now func() time.Time) inlineModel {
	return inlineModel{
		spinner:   spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(lipgloss.NewStyle().Foreground(lipgloss.Yellow))),
		now:       now,
		interrupt: interruptSelf,
	}
}

// interruptSelf stands in for the ctrl+c the terminal no longer turns into a signal while Bubble
// Tea holds it in raw mode, so the signal.NotifyContext in cli.Main still cancels the run.
func interruptSelf() {
	if p, err := os.FindProcess(os.Getpid()); err == nil {
		p.Signal(os.Interrupt)
	}
}

func (m inlineModel) Init() tea.Cmd { return m.spinner.Tick }

func (m inlineModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case drawMsg:
		return m.draw(msg.e)
	case promptMsg:
		return m.openPrompt(msg)
	case handoffMsg:
		run := tea.ExecProcess(msg.cmd, func(err error) tea.Msg { return handoffDone{err, msg.reply} })
		if b := handoffBanner(msg.why); b != "" {
			return m, tea.Sequence(tea.Println(b), run)
		}
		return m, run
	case handoffDone:
		msg.reply <- msg.err
		return m, nil
	case closeMsg:
		if text := CloseText(msg.todos, m.log); text != "" {
			return m, tea.Sequence(tea.Println(strings.TrimRight(text, "\n")), tea.Quit)
		}
		return m, tea.Quit
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case tea.KeyPressMsg:
		if m.form == nil && msg.String() == "ctrl+c" {
			m.interrupted = true
			m.interrupt()
			return m, nil
		}
	}
	return m.toForm(msg)
}

func (m inlineModel) draw(e Event) (tea.Model, tea.Cmd) {
	switch e.Kind {
	case KGroup:
		return m, m.print(groupText(e.Name))
	case KNote:
		return m, m.print(NoteText(e.Name, e.Text))
	case KTable:
		if e.Table != nil {
			return m, m.print(TableText(*e.Table))
		}
	case KLog:
		m.log = e.Text
	case KLine:
		if i := m.find(e.ID); i >= 0 {
			s := &m.steps[i]
			s.lines = append(append([]string(nil), s.lines...), strings.TrimRight(e.Text, "\r\n"))
			if len(s.lines) > tailLines {
				s.lines = s.lines[len(s.lines)-tailLines:]
			}
		}
	case KStep:
		if e.State == Running {
			m.live(e)
			return m, nil
		}
		if i := m.find(e.ID); i >= 0 {
			m.steps = append(append([]liveStep(nil), m.steps[:i]...), m.steps[i+1:]...)
		}
		return m, m.print(RowText(e, nameWidth, true))
	}
	return m, nil
}

func (m *inlineModel) find(id string) int {
	for i, s := range m.steps {
		if s.id == id {
			return i
		}
	}
	return -1
}

func (m *inlineModel) live(e Event) {
	i := m.find(e.ID)
	if i < 0 {
		m.steps = append(m.steps[:len(m.steps):len(m.steps)], liveStep{id: e.ID, name: e.Name, start: m.now()})
		i = len(m.steps) - 1
	} else {
		m.steps = append([]liveStep(nil), m.steps...)
	}
	s := &m.steps[i]
	if e.Detail != "" {
		s.detail = e.Detail
	}
	if e.Total > 0 {
		s.n, s.total = e.N, e.Total
	}
}

// print is a line for the scrollback; behind an open form it waits, so rows keep the order they
// ended in and nothing prints between a question and its answer.
func (m *inlineModel) print(text string) tea.Cmd {
	if m.form != nil {
		m.queue = append(m.queue, text)
		return nil
	}
	return tea.Println(text)
}

func groupText(title string) string { return "\n" + Out{Color: true}.Bold(title) }

func (m inlineModel) openPrompt(msg promptMsg) (tea.Model, tea.Cmd) {
	m.form, m.result = formFor(msg.req)
	m.reply = msg.reply
	return m, m.form.Init()
}

// formFor embeds the request as a huh form; result reads the answer once the form completes.
func formFor(r PromptReq) (*huh.Form, func() any) {
	if r.Kind == PForm {
		return r.Form.WithTheme(huh.ThemeFunc(Theme)), func() any { return nil }
	}
	desc := strings.Join(append(append([]string(nil), r.Q.Facts...), r.Q.Help), "\n")
	desc = strings.Trim(desc, "\n")
	var (
		f      huh.Field
		result func() any
	)
	switch r.Kind {
	case PConfirm:
		b := r.Q.Default == "yes"
		f = huh.NewConfirm().Title(r.Q.Title).Description(desc).Value(&b)
		result = func() any { return b }
	case PChoose:
		var i int
		opts := make([]huh.Option[int], len(r.Options))
		for j, o := range r.Options {
			opts[j] = huh.NewOption(o, j)
		}
		f = huh.NewSelect[int]().Title(r.Q.Title).Description(desc).Options(opts...).Value(&i)
		result = func() any { return i }
	default:
		s := r.Q.Default
		in := huh.NewInput().Title(r.Q.Title).Description(desc).Value(&s)
		if r.Kind == PSecret {
			in = in.EchoMode(huh.EchoModeNone)
		}
		f = in
		result = func() any { return s }
	}
	return Form(huh.NewGroup(f)), result
}

// toForm gives the open form every message it has not been handed already, and closes it when
// it ends.
func (m inlineModel) toForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.form == nil {
		return m, nil
	}
	cmd, reply, ended := advance(m.form, m.result, msg)
	if !ended {
		return m, cmd
	}
	m.reply <- reply
	m.form, m.result, m.reply = nil, nil, nil
	queued := m.queue
	m.queue = nil
	if len(queued) == 0 {
		return m, cmd
	}
	return m, tea.Sequence(cmd, tea.Println(strings.Join(queued, "\n")))
}

func (m inlineModel) View() tea.View {
	width := m.width
	if width <= 0 {
		width = 80
	}
	var b strings.Builder
	out := Out{Color: true}
	for _, s := range m.steps {
		detail := s.detail
		if s.total > 0 {
			detail = strings.TrimSpace(fmt.Sprintf("%d/%d %s", s.n, s.total, detail))
		}
		suffix := "   " + clockFmt(m.now().Sub(s.start))
		fixed := utf8.RuneCountInString(fmt.Sprintf("%s  %-*s %s", indent, nameWidth, s.name, suffix))
		detail = cut(detail, max(width-2-fixed, 1))
		line := strings.TrimRight(fmt.Sprintf("%s%s %-*s %s", indent, m.spinner.View(), nameWidth, s.name, detail), " ")
		b.WriteString(line + suffix + "\n")
		for _, l := range s.lines {
			b.WriteString(out.paint("2", cut(strings.Repeat(" ", 6)+l, width-2)) + "\n")
		}
	}
	if m.form != nil {
		b.WriteString("\n" + m.form.View())
	}
	return tea.NewView(b.String())
}

// cut shortens s to n columns with an ellipsis. A line that wrapped would leave a copy of itself
// behind on every redraw, and the last column is left empty because terminals differ on wrapping
// there.
func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:max(n-1, 0)]) + "…"
	}
	return s
}

type inline struct {
	p    *tea.Program
	done chan struct{}
}

var errInlineEnded = errors.New("the terminal display ended before the question was answered")

// NewInline draws live steps under the scrollback on out and reads keys from in. The program
// leaves signals alone: its ctrl+c arrives as a key, and the model re-sends it as SIGINT so the
// run's own signal handling sees it.
func NewInline(out io.Writer, in io.Reader) Renderer { return newInline(out, in) }

func newInline(out io.Writer, in io.Reader, opts ...tea.ProgramOption) Renderer {
	r := &inline{done: make(chan struct{})}
	opts = append([]tea.ProgramOption{tea.WithOutput(out), tea.WithInput(in), tea.WithoutSignalHandler()}, opts...)
	r.p = tea.NewProgram(newInlineModel(time.Now), opts...)
	go func() {
		defer close(r.done)
		r.p.Run()
	}()
	return r
}

func (r *inline) Draw(e Event) { r.p.Send(drawMsg{e}) }

func (r *inline) Prompt(p PromptReq) (any, error) {
	reply := make(chan promptReply, 1)
	r.p.Send(promptMsg{req: p, reply: reply})
	select {
	case a := <-reply:
		return a.v, a.err
	case <-r.done:
		return nil, errInlineEnded
	}
}

func (r *inline) Handoff(why string, c *exec.Cmd) error {
	reply := make(chan error, 1)
	r.p.Send(handoffMsg{why: why, cmd: c, reply: reply})
	select {
	case err := <-reply:
		return err
	case <-r.done:
		return errInlineEnded
	}
}

func (r *inline) Close(todos []string) int {
	r.p.Send(closeMsg{todos})
	<-r.done
	return 0
}
