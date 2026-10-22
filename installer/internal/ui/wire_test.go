package ui

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestEncodeRoundTripsEveryKind(t *testing.T) {
	evs := []Event{
		{Kind: KGroup, Name: "Host"},
		{Kind: KStep, ID: "s1", Name: "Packages", State: Running, N: 1, Total: 3},
		{Kind: KStep, ID: "s1", Name: "Packages", State: Done, Detail: "ok"},
		{Kind: KLine, ID: "s1", Text: "apt-get update"},
		{Kind: KNote, Name: "t", Text: "body"},
		{Kind: KTodo, Text: "log in"},
		{Kind: KTable, Table: &Table{Title: "T", Headers: []string{"a"}, Rows: [][]string{{"1"}}}},
		{Kind: KPlan, Text: "a\nb"},
		{Kind: KLog, Text: "/tmp/setup.log"},
		{Kind: KData, Name: "summary", Text: `{"x":1}`},
	}
	for _, e := range evs {
		b := Encode(e)
		if !bytes.HasPrefix(b, []byte(WirePrefix)) || b[len(b)-1] != '\n' || bytes.Count(b, []byte("\n")) != 1 {
			t.Fatalf("%v: bad line %q", e.Kind, b)
		}
		var got struct {
			V int `json:"v"`
			Event
		}
		if err := json.Unmarshal(b[len(WirePrefix):], &got); err != nil || got.V != WireVersion || !reflect.DeepEqual(got.Event, e) {
			t.Errorf("%v: got %+v %v", e.Kind, got, err)
		}
	}
}

func replayed(chunks ...string) (*Recorder, *Replayer) {
	rec := &Recorder{}
	r := Replay(New(rec), "Host", "a/")
	for _, c := range chunks {
		r.Write([]byte(c))
	}
	return rec, r
}

func TestReplayHandlesChunksSplitMidLine(t *testing.T) {
	line := string(Encode(Event{Kind: KStep, ID: "h1", Name: "Packages", State: Running})) +
		string(Encode(Event{Kind: KStep, ID: "h1", Name: "Packages", State: Done, Detail: "ok"}))
	var chunks []string
	for i := 0; i < len(line); i += 7 {
		chunks = append(chunks, line[i:min(i+7, len(line))])
	}
	rec, r := replayed(chunks...)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	rows := rec.Rows()
	if len(rows) != 1 || rows[0].Name != "Packages" || rows[0].State != Done || rows[0].Detail != "ok" {
		t.Fatalf("%+v", rows)
	}
	if rec.Events[0].Kind != KGroup || rec.Events[0].Name != "Host" {
		t.Errorf("no group first: %+v", rec.Events[0])
	}
}

func TestReplayTurnsStrayLinesIntoLinesOfTheRunningStep(t *testing.T) {
	rec, r := replayed(string(Encode(Event{Kind: KStep, ID: "h1", Name: "Packages", State: Running})),
		"Reading package lists...\n",
		flocFake()+"{not json\n")
	var lines []string
	for _, e := range rec.Events {
		if e.Kind == KLine {
			lines = append(lines, e.Text)
		}
	}
	if len(lines) != 2 || lines[0] != "Reading package lists..." {
		t.Fatalf("lines %q", lines)
	}
	r.Close()
}

func flocFake() string { return WirePrefix }

func TestReplayWithoutARunningStepMakesANote(t *testing.T) {
	rec, _ := replayed("hello\n")
	last := rec.Events[len(rec.Events)-1]
	if last.Kind != KNote || last.Text != "hello" {
		t.Fatalf("%+v", last)
	}
}

func TestReplayCloseFailsARunningStepWithTheLastStrayLines(t *testing.T) {
	chunks := []string{string(Encode(Event{Kind: KStep, ID: "h1", Name: "Packages", State: Running}))}
	for i := 0; i < 25; i++ {
		chunks = append(chunks, "line "+string(rune('a'+i))+"\n")
	}
	chunks = append(chunks, "partial json "+WirePrefix+`{"type":"ste`)
	rec, r := replayed(chunks...)
	r.Close()
	rows := rec.Rows()
	if len(rows) != 1 || rows[0].State != Failed {
		t.Fatalf("%+v", rows)
	}
	got := strings.Split(rows[0].Err, "\n")
	if len(got) != 20 || got[0] != "line g" || !strings.HasPrefix(got[19], "partial json") {
		t.Errorf("err %q", rows[0].Err)
	}
}

func TestReplayCloseGivesACutOffStepAReason(t *testing.T) {
	rec, r := replayed(string(Encode(Event{Kind: KStep, ID: "h1", Name: "Packages", State: Running})))
	r.Close()
	rows := rec.Rows()
	if len(rows) != 1 || rows[0].State != Failed || rows[0].Err == "" {
		t.Fatalf("%+v", rows)
	}
}

func TestReplayKeepsTwoHostsStepsApart(t *testing.T) {
	rec := &Recorder{}
	s := New(rec)
	a, b := Replay(s, "alpha", "a/"), Replay(s, "beta", "b/")
	a.Write(Encode(Event{Kind: KStep, ID: "h1", Name: "Packages", State: Running}))
	b.Write(Encode(Event{Kind: KStep, ID: "h1", Name: "Packages", State: Running}))
	b.Write(Encode(Event{Kind: KStep, ID: "h1", Name: "Packages", State: Failed, Err: "boom"}))
	a.Write(Encode(Event{Kind: KStep, ID: "h1", Name: "Packages", State: Done}))
	rows := rec.Rows()
	if len(rows) != 2 || rows[0].State != Failed || rows[1].State != Done || rows[0].ID == rows[1].ID {
		t.Fatalf("%+v", rows)
	}
}

func TestReplayKeepsDataOutOfTheDrawing(t *testing.T) {
	rec, r := replayed(string(Encode(Event{Kind: KData, Name: "summary", Text: `{"a":1}`})),
		string(Encode(Event{Kind: KData, Name: "summary", Text: `{"a":2}`})))
	if got, ok := r.Data("summary"); !ok || got != `{"a":2}` {
		t.Errorf("got %q %v", got, ok)
	}
	if _, ok := r.Data("cardinal"); ok {
		t.Error("cardinal was never sent")
	}
	for _, e := range rec.Events {
		if e.Kind == KData {
			t.Errorf("drawn: %+v", e)
		}
	}
}

func TestReplaySanitizesFarEndTextButNotData(t *testing.T) {
	rec := &Recorder{}
	r := Replay(New(rec), "", "")
	r.Write(Encode(Event{Kind: KStep, ID: "h1", Name: "Pa\x1bck", State: Done, Detail: "d\x1b"}))
	r.Write([]byte("stray\x1b[2J\n"))
	r.Write(Encode(Event{Kind: KData, Name: "x", Text: "keep\x1b"}))
	r.Close()
	if row := rec.Rows()[0]; row.Name != "Pa ck" || row.Detail != "d " {
		t.Errorf("%+v", row)
	}
	if last := rec.Events[len(rec.Events)-1]; last.Text != "stray [2J" {
		t.Errorf("%+v", last)
	}
	if v, _ := r.Data("x"); v != "keep\x1b" {
		t.Errorf("data %q", v)
	}
}
