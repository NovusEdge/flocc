package host

import (
	"regexp"
	"strconv"
)

var semver = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// Version is the first x.y.z in s.
func Version(s string) ([3]int, bool) {
	m := semver.FindStringSubmatch(s)
	if m == nil {
		return [3]int{}, false
	}
	var v [3]int
	for i := range 3 {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v, true
}

// AtLeast reports whether the first x.y.z in s is at least min.
func AtLeast(s string, min [3]int) bool {
	v, ok := Version(s)
	if !ok {
		return false
	}
	for i := range 3 {
		if v[i] != min[i] {
			return v[i] > min[i]
		}
	}
	return true
}
