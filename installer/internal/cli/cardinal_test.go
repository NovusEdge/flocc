package cli

import (
	"context"
	"strings"
	"testing"
)

func TestCardinalRefusesAHostFlag(t *testing.T) {
	d, _, errb := testDeps(t)
	if code := run(context.Background(), d, []string{"cardinal", "-H", "flocc-nimble", "status"}); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errb.String(), "--only") {
		t.Fatalf("stderr %q", errb.String())
	}
}
