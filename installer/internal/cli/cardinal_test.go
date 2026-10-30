package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/ui"
)

func TestHostCardinalNeedsAHost(t *testing.T) {
	d, _, errb := testDeps(t)
	if code := hostCardinal(context.Background(), d, "", nil); code != 2 || !strings.Contains(errb.String(), "flocc host cardinal -H <host>") {
		t.Fatalf("%d %q", code, errb)
	}
}

func TestHostCardinalNeedsAHostOnAMachine(t *testing.T) {
	d, _, _ := testDeps(t)
	rec := &ui.Recorder{}
	d.ui = ui.New(rec)
	path := hostFile(d, "flocc-two")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte("ssh = \"root@flocc-two\"\n"), 0o600)
	if code := hostCardinal(context.Background(), d, "flocc-two", nil); code != 1 {
		t.Fatalf("exit %d", code)
	}
	rows := rec.Rows()
	if len(rows) != 1 || !strings.Contains(rows[0].Err, "no machine") {
		t.Fatalf("%+v", rows)
	}
}

func TestUsageNamesHostCardinal(t *testing.T) {
	if !strings.Contains(usage, "flocc host cardinal -H <name> [--renew-hour H]") {
		t.Fatal(usage)
	}
}
