package ui

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
)

const WirePrefix = "flocc-event "
const WireVersion = 1

type wireEvent struct {
	V int `json:"v"`
	Event
}

// Encode is one wire line for e, with the version, ending in "\n".
func Encode(e Event) []byte {
	b, _ := json.Marshal(wireEvent{V: WireVersion, Event: e}) // plain strings and numbers cannot fail
	return append([]byte(WirePrefix), append(b, '\n')...)
}

func decode(line string) (Event, bool) {
	rest, ok := strings.CutPrefix(line, WirePrefix)
	if !ok {
		return Event{}, false
	}
	var w wireEvent
	if json.Unmarshal([]byte(rest), &w) != nil || w.V != WireVersion {
		return Event{}, false
	}
	return w.Event, true
}

const strayKeep = 20

// Replayer turns a host's stdout into events on a Session.
type Replayer struct {
	s      Session
	prefix string

	mu      sync.Mutex
	buf     bytes.Buffer
	steps   map[string]*Step
	running []string // wire IDs of steps still running, oldest first
	stray   []string
	data    map[string]string
}

// Replay is an io.Writer for a host's stdout: wire lines become events on s under group, with
// step IDs prefixed by prefix so two hosts' s7 never collide; any other line becomes a KLine of
// the running step (or a note when no step runs). Close fails a step left running with the last
// 20 stray lines.
func Replay(s Session, group, prefix string) *Replayer {
	if group != "" {
		s.Group(group)
	}
	return &Replayer{s: s, prefix: prefix, steps: map[string]*Step{}, data: map[string]string{}}
}

func (r *Replayer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf.Write(p)
	for {
		i := bytes.IndexByte(r.buf.Bytes(), '\n')
		if i < 0 {
			return len(p), nil
		}
		line := string(r.buf.Next(i + 1))
		r.line(strings.TrimRight(line, "\r\n"))
	}
}

// Close handles a partial last line, then fails every step the stream left running.
func (r *Replayer) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.buf.Len() > 0 {
		r.line(strings.TrimRight(r.buf.String(), "\r\n"))
		r.buf.Reset()
	}
	if len(r.running) == 0 {
		return nil
	}
	err := errors.New(cmp.Or(strings.Join(r.stray, "\n"), "the host stream ended before this step finished"))
	for _, id := range r.running {
		r.steps[id].Fail(err)
	}
	r.running = nil
	return nil
}

// Data is the last KData event of that name; KData is not drawn.
func (r *Replayer) Data(name string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.data[name]
	return v, ok
}

const blockLines = 200

// sanitized makes a decoded event safe to draw: the host is a far end, and its text can carry
// escape sequences. KData is parsed, not drawn, and stays as sent.
func sanitized(e Event) Event {
	e.Name, e.Detail = saved.Sanitize(e.Name), saved.Sanitize(e.Detail)
	e.Text, e.Err = saved.SanitizeBlock(e.Text, blockLines), saved.SanitizeBlock(e.Err, blockLines)
	if e.Table != nil {
		t := *e.Table
		t.Title = saved.Sanitize(t.Title)
		t.Headers = sanitizedCells(t.Headers)
		t.Rows = make([][]string, len(e.Table.Rows))
		for i, row := range e.Table.Rows {
			t.Rows[i] = sanitizedCells(row)
		}
		e.Table = &t
	}
	return e
}

func sanitizedCells(cells []string) []string {
	out := make([]string, len(cells))
	for i, c := range cells {
		out[i] = saved.Sanitize(c)
	}
	return out
}

func (r *Replayer) line(line string) {
	e, ok := decode(line)
	if !ok {
		r.strayLine(saved.Sanitize(line))
		return
	}
	if e.Kind != KData {
		e = sanitized(e)
	}
	switch e.Kind {
	case KGroup:
		r.s.Group(e.Name)
	case KStep:
		r.step(e)
	case KLine:
		if st := r.steps[r.prefix+e.ID]; st != nil {
			st.Line(e.Text)
		} else {
			r.s.Note("", e.Text)
		}
	case KNote:
		r.s.Note(e.Name, e.Text)
	case KTodo:
		r.s.Todo(e.Text)
	case KTable:
		if e.Table != nil {
			r.s.Table(*e.Table)
		}
	case KPlan:
		r.s.Plan(strings.Split(e.Text, "\n"))
	case KLog:
		r.s.LogPath(e.Text)
	case KData:
		r.data[e.Name] = e.Text
	}
}

func (r *Replayer) step(e Event) {
	key := r.prefix + e.ID
	st := r.steps[key]
	if st == nil {
		st = r.s.Step(e.Name)
		r.steps[key] = st
		r.running = append(r.running, key)
	}
	switch e.State {
	case Running:
		if e.Detail != "" {
			st.Detail(e.Detail)
		}
		if e.Total > 0 {
			st.Progress(e.N, e.Total)
		}
		return
	case Done:
		st.Done(e.Detail)
	case Pending:
		st.Pending(e.Detail)
	case Skipped:
		st.Skip(e.Detail)
	default:
		st.Fail(errors.New(e.Err))
	}
	for i, k := range r.running {
		if k == key {
			r.running = append(r.running[:i], r.running[i+1:]...)
			break
		}
	}
}

func (r *Replayer) strayLine(line string) {
	r.stray = append(r.stray, line)
	if len(r.stray) > strayKeep {
		r.stray = r.stray[1:]
	}
	if n := len(r.running); n > 0 {
		r.steps[r.running[n-1]].Line(line)
		return
	}
	r.s.Note("", line)
}
