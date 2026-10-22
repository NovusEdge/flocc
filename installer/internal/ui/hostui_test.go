package ui

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/answers"
)

func TestEventsWritesWireLinesOnly(t *testing.T) {
	var out bytes.Buffer
	e := &Events{Scripted: Scripted{A: &answers.Answers{}, Out: &out}, W: &out}
	e.Status([]StepStatus{{Name: "A", Detail: "fine", Done: true}, {Name: "B"}})
	e.Step(Line{N: 1, Total: 2, Name: "B"}, func() (Outcome, error) { return Outcome{Detail: "made"}, nil })
	e.Step(Line{N: 2, Total: 2, Name: "C"}, func() (Outcome, error) { return Outcome{Pending: true, Detail: "later"}, nil })
	err := e.Step(Line{N: 3, Total: 3, Name: "D"}, func() (Outcome, error) { return Outcome{}, errors.New("boom") })
	if err == nil {
		t.Fatal("the step's error was swallowed")
	}
	e.Note("t", "body")
	e.Data("summary", `{"a":1}`)
	if strings.Contains(out.String(), "\x1b") {
		t.Fatalf("escape in %q", out.String())
	}

	rec := &Recorder{}
	r := Replay(New(rec), "", "")
	r.Write(out.Bytes())
	r.Close()
	var got []string
	for _, row := range rec.Rows() {
		got = append(got, row.Name+":"+string(row.State)+":"+row.Detail+row.Err)
	}
	want := []string{"A:done:fine", "B:done:made", "C:pending:later", "D:failed:boom"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("rows %q, want %q", got, want)
	}
	if v, ok := r.Data("summary"); !ok || v != `{"a":1}` {
		t.Errorf("data %q %v", v, ok)
	}
	if last := rec.Events[len(rec.Events)-1]; last.Kind != KNote || last.Name != "t" {
		t.Errorf("last drawn event %+v", last)
	}
}
