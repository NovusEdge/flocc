package ui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

// ErrNoTerminal is what a prompt returns when its input ends before an answer.
var ErrNoTerminal = errors.New("flocc asks here; run it in a terminal")

const (
	nameWidth = 14
	slowAfter = 5 * time.Second
	maxTries  = 3
)

// plain prints ended rows and prompts as lines, with no live state, so a pipe or a log reads
// the same as a terminal.
type plain struct {
	out   io.Writer
	rawIn io.Reader
	in    *bufio.Reader // one reader for the renderer's life, so buffered input survives between prompts
	color bool
	now   func() time.Time

	wrote bool
	log   string
	start map[string]time.Time
	shown map[string]bool
}

func NewPlain(out io.Writer, in io.Reader, color bool) Renderer {
	return &plain{out: out, rawIn: in, in: bufio.NewReader(in), color: color, now: time.Now,
		start: map[string]time.Time{}, shown: map[string]bool{}}
}

func (p *plain) println(s string) {
	p.wrote = true
	fmt.Fprintln(p.out, s)
}

func (p *plain) blank() {
	if p.wrote {
		fmt.Fprintln(p.out)
	}
}

func (p *plain) Draw(e Event) {
	switch e.Kind {
	case KGroup:
		p.blank()
		p.println(e.Name)
	case KStep:
		if e.State == Running {
			p.slow(e)
			return
		}
		delete(p.start, e.ID)
		p.println(RowText(e, nameWidth, p.color))
	case KNote:
		p.blank()
		p.println(NoteText(e.Name, e.Text))
	case KTable:
		if e.Table != nil {
			p.println(TableText(*e.Table))
		}
	case KLog:
		p.log = e.Text
	}
}

// slow prints one line for a step still running after slowAfter, at its first detail past it,
// so a slow step shows in a log.
func (p *plain) slow(e Event) {
	t, ok := p.start[e.ID]
	if !ok {
		p.start[e.ID] = p.now()
		return
	}
	if e.Detail == "" || p.shown[e.ID] || p.now().Sub(t) < slowAfter {
		return
	}
	p.shown[e.ID] = true
	p.println(fmt.Sprintf("%s%s %s  %s", indent, Mark(Running, p.color), e.Name, e.Detail))
}

func (p *plain) header(q Question) {
	p.blank()
	p.println(q.Title)
	for _, f := range q.Facts {
		p.println(indent + f)
	}
	if q.Help != "" {
		p.println(indent + q.Help)
	}
}

// readLine writes the newline the user typed, which a log or pipe never echoes.
func (p *plain) readLine() (string, error) {
	s, err := p.in.ReadString('\n')
	if err != nil && s == "" {
		return "", ErrNoTerminal
	}
	fmt.Fprintln(p.out)
	return strings.TrimSpace(s), nil
}

func (p *plain) Prompt(r PromptReq) (any, error) {
	switch r.Kind {
	case PConfirm:
		return p.confirm(r.Q)
	case PAsk:
		p.header(r.Q)
		if r.Q.Default != "" {
			fmt.Fprintf(p.out, "[%s]: ", r.Q.Default)
		} else {
			fmt.Fprint(p.out, ": ")
		}
		s, err := p.readLine()
		if err == nil && s == "" {
			s = r.Q.Default
		}
		return s, err
	case PSecret:
		p.header(r.Q)
		fmt.Fprint(p.out, ": ")
		return p.secret()
	case PChoose:
		return p.choose(r.Q, r.Options)
	case PForm:
		// huh answers with the defaults when input ends, with no error.
		in := &eofReader{r: p.in}
		if err := r.Form.WithAccessible(true).WithInput(in).WithOutput(p.out).Run(); err != nil {
			return nil, err
		}
		if in.eof {
			return nil, ErrNoTerminal
		}
		return nil, nil
	}
	return nil, fmt.Errorf("plain: unknown prompt kind %d", r.Kind)
}

type eofReader struct {
	r   io.Reader
	eof bool
}

func (e *eofReader) Read(b []byte) (int, error) {
	n, err := e.r.Read(b)
	if err == io.EOF {
		e.eof = true
	}
	return n, err
}

func (p *plain) confirm(q Question) (bool, error) {
	def := q.Default == "yes"
	label := "[y/N]: "
	if def {
		label = "[Y/n]: "
	}
	p.header(q)
	for range maxTries {
		fmt.Fprint(p.out, label)
		s, err := p.readLine()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(s) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
	}
	return false, errors.New("no yes or no answer")
}

func (p *plain) choose(q Question, options []string) (int, error) {
	p.header(q)
	for i, o := range options {
		p.println(fmt.Sprintf("%s%d. %s", indent, i+1, o))
	}
	for range maxTries {
		fmt.Fprintf(p.out, "Choose [1-%d]: ", len(options))
		s, err := p.readLine()
		if err != nil {
			return 0, err
		}
		if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(options) {
			return n - 1, nil
		}
	}
	return 0, errors.New("no valid choice")
}

func (p *plain) secret() (string, error) {
	if f, ok := p.rawIn.(*os.File); ok && f == os.Stdin && term.IsTerminal(int(f.Fd())) {
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(p.out)
		return string(b), err
	}
	return p.readLine()
}

func (p *plain) Handoff(why string, c *exec.Cmd) error {
	p.println("── " + why + " ──")
	if c.Stdin == nil {
		c.Stdin = os.Stdin
	}
	if c.Stdout == nil {
		c.Stdout = os.Stdout
	}
	if c.Stderr == nil {
		c.Stderr = os.Stderr
	}
	return c.Run()
}

func (p *plain) Close(todos []string) int {
	fmt.Fprint(p.out, CloseText(todos, p.log))
	return 0
}
