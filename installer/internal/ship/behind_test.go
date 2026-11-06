package ship

import "testing"

func TestBehind(t *testing.T) {
	const a, b = "aaaaaaa1111111111111111111111111111111aa", "bbbbbbb2222222222222222222222222222222bb"
	for _, tc := range []struct {
		name  string
		rep   Report
		rev   string
		clean bool
		want  bool
	}{
		{"same commit", Report{V: 1, Commit: a}, a, true, false},
		{"different commit", Report{V: 1, Commit: a}, b, true, true},
		{"no commit reported", Report{V: 1}, a, true, true},
		{"no commit, unstamped build", Report{V: 1}, "", false, true},
		{"unversioned report", Report{Commit: a}, a, true, true},
		{"different commit, dirty build", Report{V: 1, Commit: a}, b, false, false},
		{"different commit, unstamped build", Report{V: 1, Commit: a}, "", false, false},
	} {
		if got := behind(&tc.rep, tc.rev, tc.clean); got != tc.want {
			t.Errorf("%s: behind = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestShortCommit(t *testing.T) {
	if got := ShortCommit("0123456789"); got != "0123456" {
		t.Errorf("%q", got)
	}
	if got := ShortCommit("abc"); got != "abc" {
		t.Errorf("%q", got)
	}
}
