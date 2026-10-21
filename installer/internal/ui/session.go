package ui

import (
	"cmp"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"charm.land/huh/v2"
)

type Question struct {
	Title   string
	Facts   []string // shown with the question; the modal covers the rows they came from
	Help    string
	Default string // Ask; "yes"/"no" for Confirm
}

type PromptKind int

const (
	PConfirm PromptKind = iota
	PAsk
	PSecret
	PChoose
	PForm
)

type PromptReq struct {
	Kind    PromptKind
	Q       Question
	Options []string  // PChoose
	Form    *huh.Form // PForm
}

type Renderer interface {
	Draw(e Event)                          // called from the session goroutine only
	Prompt(p PromptReq) (any, error)       // blocks until answered
	Handoff(why string, c *exec.Cmd) error // the terminal goes to c
	Close(todos []string) int              // record; returns nothing about failures
}

type Session interface {
	Group(title string)
	Step(name string) *Step
	Note(title, body string)
	Todo(text string)
	Confirm(q Question) (bool, error)
	Ask(q Question) (string, error)
	Secret(q Question) (string, error)
	Choose(q Question, options []string) (int, error)
	Form(f *huh.Form) error
	Handoff(why string, c *exec.Cmd) error
	Table(t Table)
	Plan(groups []string) // the sections to come; only full-screen draws them
	LogPath(p string)     // named in the record at the end
	Failed() bool         // any step failed or ended pending
	Close() int           // 1 when a step failed or is pending, else 0
}

// session serialises every call into Renderer.Draw under one mutex, so a renderer sees events in
// one order and never from two goroutines at once. Prompts and handoffs hold no lock while they
// block: a parallel step may end meanwhile, and the renderer queues its row.
type session struct {
	r      Renderer
	mu     sync.Mutex
	next   int
	open   map[string]*Step
	todos  []string
	failed bool
	closed bool
}

func New(r Renderer) Session { return &session{r: r, open: map[string]*Step{}} }

type Step struct {
	s     *session
	id    string
	name  string
	ended bool
}

func (s *session) draw(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drawLocked(e)
}

func (s *session) drawLocked(e Event) {
	if !s.closed {
		s.r.Draw(e)
	}
}

func (s *session) Group(title string) { s.draw(Event{Kind: KGroup, Name: title}) }

func (s *session) Step(name string) *Step {
	s.mu.Lock()
	s.next++
	st := &Step{s: s, id: fmt.Sprintf("s%d", s.next), name: name}
	s.open[st.id] = st
	s.mu.Unlock()
	s.draw(Event{Kind: KStep, ID: st.id, Name: name, State: Running})
	return st
}

func (s *session) Note(title, body string) { s.draw(Event{Kind: KNote, Name: title, Text: body}) }

func (s *session) Todo(text string) {
	s.mu.Lock()
	s.todos = append(s.todos, text)
	s.mu.Unlock()
	s.draw(Event{Kind: KTodo, Text: text})
}

func (s *session) Table(t Table) { s.draw(Event{Kind: KTable, Table: &t}) }

func (s *session) Plan(groups []string) { s.draw(Event{Kind: KPlan, Text: strings.Join(groups, "\n")}) }
func (s *session) LogPath(p string)     { s.draw(Event{Kind: KLog, Text: p}) }

// Rows draws rows that were decided elsewhere (a host summary, a cardinal report) as steps.
func Rows(s Session, evs []Event) {
	for _, e := range evs {
		st := s.Step(e.Name)
		switch e.State {
		case Done:
			st.Done(e.Detail)
		case Pending:
			st.Pending(e.Detail)
		case Skipped:
			st.Skip(e.Detail)
		default:
			st.Fail(errors.New(cmp.Or(e.Err, e.Detail)))
		}
	}
}

func (s *session) prompt(p PromptReq) (any, error) { return s.r.Prompt(p) }

func (s *session) Confirm(q Question) (bool, error) {
	v, err := s.prompt(PromptReq{Kind: PConfirm, Q: q})
	b, _ := v.(bool)
	return b, err
}

func (s *session) Ask(q Question) (string, error) {
	v, err := s.prompt(PromptReq{Kind: PAsk, Q: q})
	str, _ := v.(string)
	return str, err
}

func (s *session) Secret(q Question) (string, error) {
	v, err := s.prompt(PromptReq{Kind: PSecret, Q: q})
	str, _ := v.(string)
	return str, err
}

func (s *session) Choose(q Question, options []string) (int, error) {
	v, err := s.prompt(PromptReq{Kind: PChoose, Q: q, Options: options})
	i, _ := v.(int)
	return i, err
}

func (s *session) Form(f *huh.Form) error {
	_, err := s.prompt(PromptReq{Kind: PForm, Form: f})
	return err
}

func (s *session) Handoff(why string, c *exec.Cmd) error { return s.r.Handoff(why, c) }

func (s *session) Failed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failed
}

func (s *session) Close() int {
	s.mu.Lock()
	open := make([]*Step, 0, len(s.open))
	for _, st := range s.open {
		open = append(open, st)
	}
	s.mu.Unlock()
	for _, st := range open {
		st.end(Event{State: Failed, Err: "did not finish"})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	code := s.r.Close(s.todos)
	if s.failed {
		code = 1
	}
	return code
}

func (st *Step) send(e Event) {
	e.Kind, e.ID, e.Name = cmp.Or(e.Kind, KStep), st.id, st.name
	st.s.mu.Lock()
	defer st.s.mu.Unlock()
	if !st.ended {
		st.s.drawLocked(e)
	}
}

func (st *Step) Detail(text string)    { st.send(Event{State: Running, Detail: text}) }
func (st *Step) Progress(n, total int) { st.send(Event{State: Running, N: n, Total: total}) }
func (st *Step) Line(text string)      { st.send(Event{Kind: KLine, Text: text}) }
func (st *Step) Done(detail string)    { st.end(Event{State: Done, Detail: detail}) }
func (st *Step) Pending(detail string) { st.end(Event{State: Pending, Detail: detail}) }
func (st *Step) Skip(detail string)    { st.end(Event{State: Skipped, Detail: detail}) }
func (st *Step) Fail(err error)        { st.end(Event{State: Failed, Err: err.Error()}) }

// end sends the step's last event; any later call is dropped.
func (st *Step) end(e Event) {
	st.s.mu.Lock()
	defer st.s.mu.Unlock()
	if st.ended {
		return
	}
	st.ended = true
	delete(st.s.open, st.id)
	if e.State == Failed || e.State == Pending {
		st.s.failed = true
	}
	e.Kind, e.ID, e.Name = KStep, st.id, st.name
	st.s.drawLocked(e)
}
