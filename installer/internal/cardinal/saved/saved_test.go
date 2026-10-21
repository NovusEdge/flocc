package saved

import (
	"strings"
	"testing"
)

func TestSavedRoundTripsAndKeepsRemoved(t *testing.T) {
	dir := t.TempDir()
	s := Saved{Machine: "owner@zer0-day-nimble", User: "cardinal", Hosts: []string{"flocc-nimble"}, Key: "ssh-ed25519 AAAA cardinal@zer0-day-nimble", RenewHour: 4, Removed: true}
	if err := s.Save(dir); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir, "cardinal")
	if err != nil || got.Machine != s.Machine || !got.Removed || got.Key != s.Key || len(got.Hosts) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := Load(dir, "../x"); err == nil {
		t.Fatal("loaded a path as a user")
	}
}

func TestDestUsesTheHostPartOnly(t *testing.T) {
	if d := (Saved{Machine: "owner@zer0-day-nimble", User: "cardinal"}).Dest(); d != "cardinal@zer0-day-nimble" {
		t.Fatal(d)
	}
	if d := (Saved{Machine: "zer0-day-nimble", User: "cardinal-test"}).Dest(); d != "cardinal-test@zer0-day-nimble" {
		t.Fatal(d)
	}
}

func TestSanitizeStripsEscapesAndCuts(t *testing.T) {
	got := Sanitize("ok\x1b]52;c;aGk=\x07\x1b[2J" + strings.Repeat("x", 300))
	if strings.ContainsAny(got, "\x1b\x07") || len([]rune(got)) > maxField+1 {
		t.Fatalf("%q", got)
	}
}
