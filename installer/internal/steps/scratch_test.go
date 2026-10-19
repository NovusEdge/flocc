package steps

import (
	"context"
	"os"
	"testing"

	"github.com/NovusEdge/linear-agents/installer/internal/host"
)

func TestScratchWritesTheTmpfilesRuleInSystemdMode(t *testing.T) {
	e := testEnv(t, host.NewFake())
	if done, _, _ := (Scratch{}).Check(context.Background(), e); done {
		t.Fatal("done before apply")
	}
	if err := (Scratch{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(e.Path(tmpfilesConf))
	if string(b) != tmpfilesLine {
		t.Fatalf("%q", b)
	}
	if done, _, _ := (Scratch{}).Check(context.Background(), e); !done {
		t.Fatal("not done after apply")
	}
}

func TestScratchIsSkippedInLoopMode(t *testing.T) {
	e := testEnv(t, host.NewFake())
	e.Mode = Loop
	if done, detail, _ := (Scratch{}).Check(context.Background(), e); !done || detail != "loop mode; cleaned by the service" {
		t.Fatalf("%v %q", done, detail)
	}
}
