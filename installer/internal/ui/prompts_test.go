package ui

import "testing"

func TestPromptsPassTitleHelpAndDefaultToTheSession(t *testing.T) {
	rec := &Recorder{Answers: []any{"k", true, "x", 1}}
	p := Prompts{S: New(rec)}
	if v, err := p.Secret("t", "h"); err != nil || v != "k" {
		t.Fatalf("Secret: %q %v", v, err)
	}
	if v, err := p.Confirm("c", true); err != nil || !v {
		t.Fatalf("Confirm: %v %v", v, err)
	}
	if v, err := p.Ask("a", "help"); err != nil || v != "x" {
		t.Fatalf("Ask: %q %v", v, err)
	}
	if v, err := p.Choose("ch", []string{"a", "b"}); err != nil || v != 1 {
		t.Fatalf("Choose: %d %v", v, err)
	}
	if q := rec.Asked[0].Q; q.Title != "t" || q.Help != "h" {
		t.Fatalf("secret question %+v", q)
	}
	if q := rec.Asked[1].Q; q.Default != "yes" {
		t.Fatalf("confirm default %q", q.Default)
	}
}
