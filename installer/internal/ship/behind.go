package ship

import "runtime/debug"

// SelfBuild is the commit this flocc was built from, empty without VCS stamps (as under go test).
// It is a var so tests can stand in for a stamped build. vcs.modified is ignored: it is true for any
// untracked file, and ship publishes HEAD, so uncommitted edits never reach a host anyway.
var SelfBuild = func() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" {
				return s.Value
			}
		}
	}
	return ""
}

// Behind reports whether a host runs code other than what this flocc was built from. A report
// without a commit is behind: the host predates the field or cannot tell. An unstamped build has no
// commit to compare, so it judges nothing beyond that.
func Behind(r *Report) bool {
	return behind(r, SelfBuild())
}

func behind(r *Report, rev string) bool {
	if r.V == 0 || r.Commit == "" {
		return true
	}
	return rev != "" && r.Commit != rev
}
