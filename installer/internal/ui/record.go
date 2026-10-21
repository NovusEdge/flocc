package ui

import (
	"errors"
	"os/exec"
)

// Recorder keeps what a session sends, for tests. Answers are returned to prompts in order; a
// prompt past the end gets ErrNoAnswer.
type Recorder struct {
	Events   []Event
	Answers  []any
	Asked    []PromptReq
	Handoffs []string
	Todos    []string
	Closed   bool
}

var ErrNoAnswer = errors.New("recorder: no answer left")

func (r *Recorder) Draw(e Event) { r.Events = append(r.Events, e) }

func (r *Recorder) Prompt(p PromptReq) (any, error) {
	r.Asked = append(r.Asked, p)
	if len(r.Answers) == 0 {
		return nil, ErrNoAnswer
	}
	v := r.Answers[0]
	r.Answers = r.Answers[1:]
	if err, ok := v.(error); ok {
		return nil, err
	}
	return v, nil
}

func (r *Recorder) Handoff(why string, c *exec.Cmd) error {
	r.Handoffs = append(r.Handoffs, why)
	return nil
}

func (r *Recorder) Close(todos []string) int { r.Todos, r.Closed = todos, true; return 0 }

// Rows is every step's last event, in the order the steps ended.
func (r *Recorder) Rows() []Event {
	var out []Event
	for _, e := range r.Events {
		if e.Kind == KStep && e.State != Running {
			out = append(out, e)
		}
	}
	return out
}
