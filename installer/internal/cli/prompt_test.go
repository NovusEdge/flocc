package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/linearapi"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

// huh answers a Select on a piped stdin with its first option and no error, so host add would map
// the host to the first team without asking. The plain renderer must refuse instead.
func TestSessionPromptRefusesAStdinThatEnds(t *testing.T) {
	p := sessionPrompt{ui.Prompts{S: ui.New(ui.NewPlain(&strings.Builder{}, strings.NewReader(""), false))}}
	if _, err := p.Team([]linearapi.Team{{Key: "NF"}, {Key: "OT"}}); err == nil {
		t.Fatal("Team picked without an answer")
	}
	if err := p.Repos(&answers.Answers{}, nil, nil); err == nil {
		t.Fatal("Repos returned without an answer")
	}
	if _, err := p.Ask("t", ""); !errors.Is(err, ui.ErrNoTerminal) {
		t.Fatalf("Ask: %v", err)
	}
}
