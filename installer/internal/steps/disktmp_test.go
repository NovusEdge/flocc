package steps

import (
	"context"
	"os"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

func TestDiskTmpMasksTmpMountInSystemdMode(t *testing.T) {
	e := testEnv(t, host.NewFake())
	if done, _, _ := (DiskTmp{}).Check(context.Background(), e); done {
		t.Fatal("done before apply")
	}
	if err := (DiskTmp{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if dest, _ := os.Readlink(e.Path(tmpMountMask)); dest != "/dev/null" {
		t.Fatalf("%q", dest)
	}
	if done, _, _ := (DiskTmp{}).Check(context.Background(), e); !done {
		t.Fatal("not done after apply")
	}
	if err := (DiskTmp{}).Apply(context.Background(), e); err != nil {
		t.Fatalf("second apply: %v", err)
	}
}

func TestDiskTmpIsSkippedInLoopMode(t *testing.T) {
	e := testEnv(t, host.NewFake())
	e.Mode = Loop
	if done, _, _ := (DiskTmp{}).Check(context.Background(), e); !done {
		t.Fatal("loop mode should be done")
	}
}
