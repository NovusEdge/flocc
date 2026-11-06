package ship

import "testing"

func TestBehind(t *testing.T) {
	const a, b = "aaaaaaa1111111111111111111111111111111aa", "bbbbbbb2222222222222222222222222222222bb"
	for _, tc := range []struct {
		name string
		rep  Report
		rev  string
		want bool
	}{
		{"same commit", Report{V: 1, Commit: a}, a, false},
		{"different commit", Report{V: 1, Commit: a}, b, true},
		{"no commit reported", Report{V: 1}, a, true},
		{"no commit, unstamped build", Report{V: 1}, "", true},
		{"unversioned report", Report{Commit: a}, a, true},
		{"different commit, unstamped build", Report{V: 1, Commit: a}, "", false},
	} {
		if got := behind(&tc.rep, tc.rev); got != tc.want {
			t.Errorf("%s: behind = %v, want %v", tc.name, got, tc.want)
		}
	}
}
