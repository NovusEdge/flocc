package ship

import "runtime/debug"

// SelfBuild is the commit this flocc was built from and whether the tree was clean. It is a var so
// tests can stand in for a stamped build; under go test there are no VCS stamps and rev is empty.
var SelfBuild = func() (rev string, clean bool) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	modified := false
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	return rev, rev != "" && !modified
}

// ShortCommit is the first seven characters of a commit, the form every screen shows.
func ShortCommit(sha string) string {
	return sha[:min(7, len(sha))]
}

// Behind reports whether a host runs code other than what this flocc would ship. A report without a
// commit is behind: the host predates the field or cannot tell. A dirty or unstamped build has no
// commit to compare, so it judges nothing beyond that.
func Behind(r *Report) bool {
	rev, clean := SelfBuild()
	return behind(r, rev, clean)
}

func behind(r *Report, rev string, clean bool) bool {
	if r.V == 0 || r.Commit == "" {
		return true
	}
	return clean && r.Commit != rev
}
