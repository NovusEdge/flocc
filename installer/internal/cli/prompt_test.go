package cli

import (
	"errors"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/keys"
	"github.com/NovusEdge/flocc/installer/internal/linearapi"
)

// On a piped stdin huh answers a Select with its first option and no error, so host add would
// map the host to the first team without asking.
func TestHuhPromptRefusesAStdinThatIsNoTerminal(t *testing.T) {
	p := huhPrompt{keys.Huh{Terminal: func() bool { return false }}}
	if _, err := p.Team([]linearapi.Team{{Key: "NF"}, {Key: "OT"}}); !errors.Is(err, errNoTerminal) {
		t.Fatalf("Team: %v", err)
	}
	if err := p.Repos(&answers.Answers{}, nil, nil); !errors.Is(err, errNoTerminal) {
		t.Fatalf("Repos: %v", err)
	}
	p.Note("title", "body")
}
