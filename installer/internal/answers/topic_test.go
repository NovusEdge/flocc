package answers

import (
	"strings"
	"testing"
)

func TestNormalizeTopic(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"la-abc_123", "la-abc_123", true},
		{"  la-abc \n", "la-abc", true},
		{"https://ntfy.sh/la-abc", "la-abc", true},
		{" https://ntfy.sh/la-abc ", "la-abc", true},
		{"", "", true},
		{strings.Repeat("a", 64), strings.Repeat("a", 64), true},
		{strings.Repeat("a", 65), "", false},
		{"la-abc la-def", "", false},
		{"la/abc", "", false},
		{"la.abc", "", false},
		{"https://ntfy.sh/a/b", "", false},
		{"https://example.com/abc", "", false},
		{"https://ntfy.sh/", "", false},
		{"ntfy.sh/", "", false},
		{"HTTP://NTFY.SH/la-abc", "la-abc", true},
		{"ntfy.sh/la-abc", "la-abc", true},
		{"https://ntfy.sh/la-abc/", "la-abc", true},
		{"https://ntfy.sh/la-abc//", "", false},
	}
	for _, c := range cases {
		got, err := NormalizeTopic(c.in)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("NormalizeTopic(%q) = %q, %v; want %q, ok=%v", c.in, got, err, c.want, c.ok)
		}
	}
}

func TestOverlayRejectsBadTopicWithoutChangingAnything(t *testing.T) {
	a := &Answers{NtfyTopic: "la-keep", TeamKey: "NF"}
	err := a.Overlay(&Answers{NtfyTopic: "la-x la-y", TeamKey: "XX"})
	if err == nil {
		t.Fatal("want an error for a topic with a space")
	}
	if a.NtfyTopic != "la-keep" || a.TeamKey != "NF" {
		t.Errorf("a failed overlay changed the answers: %+v", a)
	}
}

func TestOverlaySavedDropsBadTopicAndKeepsTheRest(t *testing.T) {
	a := &Answers{}
	if a.OverlaySaved(&Answers{NtfyTopic: "la-x la-y", TeamKey: "NF"}) == nil {
		t.Fatal("want the dropped topic reported")
	}
	if a.NtfyTopic != "" || a.TeamKey != "NF" {
		t.Errorf("saved overlay: %+v", a)
	}
	if err := (&Answers{}).OverlaySaved(&Answers{NtfyTopic: "la-ok"}); err != nil {
		t.Errorf("valid saved topic reported: %v", err)
	}
}

func TestOverlayNormalizesTopic(t *testing.T) {
	a := &Answers{}
	if err := a.Overlay(&Answers{NtfyTopic: " https://ntfy.sh/la-abc "}); err != nil {
		t.Fatal(err)
	}
	if a.NtfyTopic != "la-abc" {
		t.Errorf("topic = %q", a.NtfyTopic)
	}
}

func TestRandomTopicIsValid(t *testing.T) {
	if got, err := NormalizeTopic(RandomTopic()); err != nil || got == "" {
		t.Errorf("generated topic rejected: %q, %v", got, err)
	}
}
