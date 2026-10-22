// Package dockerguard is the test check that no command could restart dockerd.
package dockerguard

import (
	"path"
	"slices"
	"strings"
	"testing"
)

func isDocker(unit string) bool {
	return slices.Contains([]string{"docker", "docker.service", "docker.socket", "dockerd"}, unit)
}

// Touches reports whether c could start, stop, reload or kill dockerd. It reads c as words,
// so a verb or unit in any order among flags and other units counts, as does one behind sudo,
// sh -c or a ; or &&.
func Touches(c string) bool {
	c = strings.NewReplacer("'", " ", `"`, " ", "`", " ", "(", " ", ")", " ", "&", ";", "|", ";", "\n", ";").Replace(c)
	for _, seg := range strings.Split(c, ";") {
		f := strings.Fields(seg)
		for i, w := range f {
			var words []string // the arguments after w, without flags
			for _, a := range f[i+1:] {
				if !strings.HasPrefix(a, "-") {
					words = append(words, a)
				}
			}
			switch base := path.Base(w); {
			case base == "systemctl":
				readOnly := []string{"is-active", "is-enabled", "is-failed", "status", "show", "cat", "list-units", "list-unit-files"}
				if len(words) > 0 && !slices.Contains(readOnly, words[0]) && slices.ContainsFunc(words[1:], isDocker) {
					return true
				}
			case base == "service" || base == "invoke-rc.d":
				if len(words) > 0 && isDocker(words[0]) && (len(words) < 2 || words[1] != "status") {
					return true
				}
			case strings.HasSuffix(w, "init.d/docker"):
				if len(words) == 0 || words[0] != "status" {
					return true
				}
			case base == "kill" || base == "pkill" || base == "killall":
				if slices.ContainsFunc(words, func(a string) bool { return isDocker(a) || strings.Contains(a, "dockerd") }) {
					return true
				}
			case base == "dockerd":
				if i == 0 || slices.Contains([]string{"sudo", "-n", "env", "exec", "nohup", "--", "-c"}, f[i-1]) {
					return true
				}
			}
		}
	}
	return false
}

// Untouched fails when any command could stop, start or reload dockerd.
func Untouched(t testing.TB, calls []string) {
	t.Helper()
	for _, c := range calls {
		if Touches(c) {
			t.Errorf("touches Docker: %s", c)
		}
	}
}
