package host

import (
	"context"
	"io"
	"slices"
)

// Fake answers commands by their String() form, for tests in this module.
type Fake struct {
	Responses map[string]Result
	Seqs      map[string][]Result
	Calls     []string
	Argv      [][]string // name and args of each Run, unjoined
	Stdins    map[string]string

	InteractiveErrs map[string]error
	InteractiveOuts map[string]string
	// Interactives holds each command Interactive ran, whole: Calls keeps only String(), which
	// has no Why.
	Interactives []Cmd

	names map[string]Result
}

func NewFake() *Fake {
	return &Fake{Responses: map[string]Result{}, Seqs: map[string][]Result{}, Stdins: map[string]string{},
		InteractiveErrs: map[string]error{}, InteractiveOuts: map[string]string{}, names: map[string]Result{}}
}

// OnName answers any Run of a command called name that no exact response covers: go build and scp
// take paths of temporary directories that a test cannot spell out.
func (f *Fake) OnName(name string, code int, stdout string) *Fake {
	f.names[name] = Result{Stdout: stdout, Code: code}
	return f
}

func (f *Fake) readStdin(c Cmd) {
	if c.Stdin != nil {
		b, _ := io.ReadAll(c.Stdin)
		f.Stdins[c.String()] = string(b)
	}
}

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
	f.Argv = append(f.Argv, append([]string{c.Name}, c.Args...))
	f.readStdin(c)
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
	if r, ok := f.names[c.Name]; ok {
		return r, nil
	}
	return Result{Stderr: "fake: no response for " + c.String(), Code: 127}, nil
}

// OnInteractive makes Interactive return err for cmd.
func (f *Fake) OnInteractive(cmd string, err error) *Fake {
	f.InteractiveErrs[cmd] = err
	return f
}

// OnInteractiveOut makes Interactive write out to the command's Stdout.
func (f *Fake) OnInteractiveOut(cmd, out string) *Fake {
	f.InteractiveOuts[cmd] = out
	return f
}

func (f *Fake) Interactive(_ context.Context, c Cmd) error {
	f.Calls = append(f.Calls, "interactive: "+c.String())
	f.Interactives = append(f.Interactives, c)
	f.readStdin(c)
	if out, ok := f.InteractiveOuts[c.String()]; ok && c.Stdout != nil {
		io.WriteString(c.Stdout, out)
	}
	return f.InteractiveErrs[c.String()]
}

func (f *Fake) Ran(cmd string) bool { return slices.Contains(f.Calls, cmd) }
