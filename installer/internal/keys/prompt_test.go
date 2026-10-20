package keys

import (
	"strings"
	"testing"
)

// huh's Confirm on a piped stdin can return its default with no error, which would let a
// default-yes recheck repeat with nobody there.
func TestHuhRefusesAStdinThatIsNoTerminal(t *testing.T) {
	h := Huh{Terminal: func() bool { return false }}
	if v, err := h.Secret("t", "h"); err == nil || v != "" || !strings.Contains(err.Error(), "run it in a terminal") {
		t.Fatalf("%q %v", v, err)
	}
	if v, err := h.Confirm("t", true); err == nil || v || !strings.Contains(err.Error(), "run it in a terminal") {
		t.Fatalf("%v %v", v, err)
	}
}
