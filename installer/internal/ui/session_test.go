package ui

import (
	"errors"
	"sync"
	"testing"
)

func TestStepEndsOnce(t *testing.T) {
	r := &Recorder{}
	s := New(r)
	st := s.Step("Packages")
	st.Done("113 packages")
	st.Fail(errors.New("late"))
	st.Done("again")
	rows := r.Rows()
	if len(rows) != 1 || rows[0].State != Done || rows[0].Detail != "113 packages" {
		t.Fatalf("rows = %+v, want one done row", rows)
	}
}

func TestCloseFailsOpenSteps(t *testing.T) {
	r := &Recorder{}
	s := New(r)
	s.Step("hung")
	if code := s.Close(); code != 1 {
		t.Errorf("Close = %d, want 1", code)
	}
	rows := r.Rows()
	if len(rows) != 1 || rows[0].State != Failed || rows[0].Err != "did not finish" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestParallelSteps(t *testing.T) {
	r := &Recorder{}
	s := New(r)
	var wg sync.WaitGroup
	for _, h := range []string{"a", "b", "c", "d"} {
		wg.Add(1)
		go func() { defer wg.Done(); st := s.Step(h); st.Detail("fetching"); st.Done("ok") }()
	}
	wg.Wait()
	s.Close()
	if n := len(r.Rows()); n != 4 {
		t.Fatalf("%d rows, want 4", n)
	}
}

func TestTodosAndCode(t *testing.T) {
	r := &Recorder{}
	s := New(r)
	s.Todo("log in on flocc-2")
	s.Step("x").Pending("waits on you")
	if code := s.Close(); code != 1 {
		t.Errorf("code %d", code)
	}
	if len(r.Todos) != 1 {
		t.Errorf("todos %v", r.Todos)
	}
}
