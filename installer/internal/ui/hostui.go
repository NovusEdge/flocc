package ui

import (
	"io"
	"strconv"
	"sync"
)

// Events is the host side of the wire: flocc-setup --events runs the steps under it and the
// desktop's Replay draws them. Answers still come from Scripted.
type Events struct {
	Scripted
	W io.Writer

	mu   sync.Mutex
	next int
}

func (e *Events) send(ev Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.W.Write(Encode(ev))
}

func (e *Events) id() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.next++
	return "h" + strconv.Itoa(e.next)
}

func (e *Events) Status(steps []StepStatus) {
	for _, st := range steps {
		ev := Event{Kind: KStep, ID: e.id(), Name: st.Name, Detail: st.Detail, State: Done}
		if !st.Done {
			ev.State, ev.Detail = Skipped, "to do"
		}
		e.send(ev)
	}
}

func (e *Events) Note(title, body string) { e.send(Event{Kind: KNote, Name: title, Text: body}) }

// Data sends machine data for the desktop; Replay keeps it under name and never draws it.
func (e *Events) Data(name, text string) { e.send(Event{Kind: KData, Name: name, Text: text}) }

func (e *Events) Step(l Line, fn func() (Outcome, error)) error {
	id := e.id()
	e.send(Event{Kind: KStep, ID: id, Name: l.Name, Detail: l.Detail, State: Running, N: l.N, Total: l.Total})
	o, err := fn()
	end := Event{Kind: KStep, ID: id, Name: l.Name, State: Done, Detail: o.Detail}
	switch {
	case err != nil:
		end.State, end.Detail, end.Err = Failed, "", err.Error()
	case o.Pending:
		end.State = Pending
	}
	e.send(end)
	return err
}
