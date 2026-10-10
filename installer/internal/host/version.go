package host

import (
	"regexp"
	"strconv"
)

var semver = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// AtLeast reports whether the first x.y.z in s is at least min.
func AtLeast(s string, min [3]int) bool {
	m := semver.FindStringSubmatch(s)
	if m == nil {
		return false
	}
	for i := range 3 {
		n, _ := strconv.Atoi(m[i+1])
		if n != min[i] {
			return n > min[i]
		}
	}
	return true
}
