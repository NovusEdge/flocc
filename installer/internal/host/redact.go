package host

import (
	"cmp"
	"io"
	"slices"
	"strings"
)

// Redact returns w with every non-empty secret replaced by <token>. It works per Write, so a
// secret split across two Writes would pass; Exec writes each command line and each output whole.
func Redact(w io.Writer, secrets ...string) io.Writer {
	// strings.Replacer prefers the earlier pair at a position, so longest first keeps a secret
	// that contains another from leaking its tail.
	keep := slices.DeleteFunc(slices.Clone(secrets), func(s string) bool { return s == "" })
	if len(keep) == 0 {
		return w
	}
	slices.SortStableFunc(keep, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	var pairs []string
	for _, s := range keep {
		pairs = append(pairs, s, "<token>")
	}
	return redactor{w, strings.NewReplacer(pairs...)}
}

type redactor struct {
	w io.Writer
	r *strings.Replacer
}

func (r redactor) Write(p []byte) (int, error) {
	if _, err := io.WriteString(r.w, r.r.Replace(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}
