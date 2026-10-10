package host

import (
	"context"
	"slices"
)

// Fake answers commands by their String() form, for tests in this module.
type Fake struct {
	Responses map[string]Result
	Seqs      map[string][]Result
	Calls     []string
}

func NewFake() *Fake { return &Fake{Responses: map[string]Result{}, Seqs: map[string][]Result{}} }

func (f *Fake) On(cmd string, code int, stdout string) *Fake {
	f.Responses[cmd] = Result{Stdout: stdout, Code: code}
	return f
}

func (f *Fake) OnSeq(cmd string, rs ...Result) *Fake {
	f.Seqs[cmd] = rs
	return f
}

func (f *Fake) Run(_ context.Context, c Cmd) (Result, error) {
	f.Calls = append(f.Calls, c.String())
	if seq := f.Seqs[c.String()]; len(seq) > 0 {
		r := seq[0]
		if len(seq) > 1 {
			f.Seqs[c.String()] = seq[1:]
		}
		return r, nil
	}
	if r, ok := f.Responses[c.String()]; ok {
		return r, nil
	}
	return Result{Stderr: "fake: no response for " + c.String(), Code: 127}, nil
}

func (f *Fake) Interactive(_ context.Context, c Cmd) error {
	f.Calls = append(f.Calls, "interactive: "+c.String())
	return nil
}

func (f *Fake) Ran(cmd string) bool { return slices.Contains(f.Calls, cmd) }
