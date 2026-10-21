package ui

import "testing"

func TestRowText(t *testing.T) {
	cases := []struct {
		e     Event
		color bool
		want  string
	}{
		{Event{Kind: KStep, Name: "Linear key", State: Done, Detail: "valid"}, false, "  ✓ Linear key     valid"},
		{Event{Kind: KStep, Name: "la-nimble", State: Pending, Detail: "1 left"}, false, "  • la-nimble      1 left"},
		{Event{Kind: KStep, Name: "x", State: Skipped, Detail: "kept"}, false, "  · x              kept"},
		{Event{Kind: KStep, Name: "x", State: Failed, Err: "boom"}, false, "  ✗ x              boom"},
		{Event{Kind: KStep, Name: "x", State: Done}, true, "  \x1b[32m✓\x1b[0m x"},
	}
	for _, c := range cases {
		if got := RowText(c.e, 14, c.color); got != c.want {
			t.Errorf("RowText(%+v) = %q, want %q", c.e, got, c.want)
		}
	}
}

func TestRowTextMultiLineDetail(t *testing.T) {
	got := RowText(Event{Kind: KStep, Name: "keys", State: Pending, Detail: "a\nb"}, 6, false)
	if want := "  • keys   a\n           b"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
