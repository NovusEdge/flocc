package host

import (
	"bytes"
	"io"
)

// Redact returns w with every non-empty secret replaced by <token>. It works per Write, so a
// secret split across two Writes would pass; Exec writes each command line and each output whole.
func Redact(w io.Writer, secrets ...string) io.Writer {
	var keep [][]byte
	for _, s := range secrets {
		if s != "" {
			keep = append(keep, []byte(s))
		}
	}
	if len(keep) == 0 {
		return w
	}
	return redactor{w, keep}
}

type redactor struct {
	w       io.Writer
	secrets [][]byte
}

func (r redactor) Write(p []byte) (int, error) {
	out := p
	for _, s := range r.secrets {
		out = bytes.ReplaceAll(out, s, []byte("<token>"))
	}
	if _, err := r.w.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}
