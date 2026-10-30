package keys

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/ui"
)

func linFromFile(v string) map[Slot]Taken { return map[Slot]Taken{Linear: {v, ".env"}} }

func TestTakeStaysQuietForAValueThatIsAlreadyStored(t *testing.T) {
	f := newFixture(t, nil, nil, &script{})
	f.store.m[Linear] = "lin_api_k"
	f.store.m[Tailscale] = "kid:tskey-client-kid-0123456789QRST"
	vals := map[Slot]Taken{Linear: {"lin_api_k", ".env"}, Tailscale: {"tskey-client-kid-0123456789QRST", ".env"}}
	changed, err := Take(context.Background(), f.env, Mapped{}, vals, fillOrder)
	if err != nil || len(changed) != 0 || len(f.rec.Events) != 0 || len(f.events) != 0 {
		t.Fatal(changed, err, f.rec.Events, f.events)
	}
}

func TestTakeChecksAndStoresANewValueWithoutCountingItAsReplaced(t *testing.T) {
	f := newFixture(t, nil, linServer(t, "viewer-ok.http"), &script{})
	changed, err := Take(context.Background(), f.env, Mapped{}, linFromFile("lin_api_k"), Slots)
	if err != nil || len(changed) != 0 {
		t.Fatal(changed, err)
	}
	if f.store.m[Linear] != "lin_api_k" {
		t.Fatalf("%v", f.store.m)
	}
	if r := f.row("Check Linear API key from .env"); r.State != ui.Done {
		t.Errorf("%+v", f.rec.Rows())
	}
	if r := f.row("Store Linear API key"); r.State != ui.Done {
		t.Errorf("%+v", f.rec.Rows())
	}
}

func TestTakeNamesTheEnvironmentWhenItSuppliedTheValue(t *testing.T) {
	f := newFixture(t, nil, linServer(t, "viewer-ok.http"), &script{})
	_, err := Take(context.Background(), f.env, Mapped{}, map[Slot]Taken{Linear: {"lin_api_k", "the environment"}}, Slots)
	if err != nil || f.row("Check Linear API key from the environment").State != ui.Done {
		t.Fatal(err, f.rec.Rows())
	}
}

func TestTakeReportsAChangedValueAsReplacedAndLeavesTheOtherSlotsAlone(t *testing.T) {
	f := newFixture(t, nil, linServer(t, "viewer-ok.http"), &script{})
	f.store.m[Linear] = "lin_api_old"
	f.store.m[Agent] = "github_pat_keep"
	vals := linFromFile("lin_api_new")
	vals[Agent] = Taken{"github_pat_other", ".env"}
	changed, err := Take(context.Background(), f.env, Mapped{}, vals, []Slot{Linear})
	if err != nil || !slices.Equal(changed, []Slot{Linear}) {
		t.Fatal(changed, err)
	}
	if f.store.m[Linear] != "lin_api_new" || f.store.m[Agent] != "github_pat_keep" {
		t.Fatalf("%v", f.store.m)
	}
}

func TestTakeSkipsAValueThatFailsItsCheckAndKeepsTheStoredKey(t *testing.T) {
	f := newFixture(t, nil, nil, &script{})
	f.store.m[Linear] = "lin_api_old"
	bad := "github_pat_SECRETVALUE0123456789"
	changed, err := Take(context.Background(), f.env, Mapped{}, linFromFile(bad), Slots)
	if err != nil || len(changed) != 0 {
		t.Fatal(changed, err)
	}
	r := f.row("Check Linear API key from .env")
	if r.State != ui.Skipped || !strings.Contains(r.Detail, "github_pat_…6789, 32 characters") || !strings.Contains(r.Detail, "that is a GitHub token") {
		t.Fatalf("%+v", r)
	}
	if f.store.m[Linear] != "lin_api_old" || f.env.UI.Failed() {
		t.Fatalf("%v", f.store.m)
	}
	if strings.Contains(f.text(), bad) || strings.Contains(f.text(), "SECRETVALUE") {
		t.Fatalf("the value reached the session:\n%s", f.text())
	}
}

func TestTakeAsksBeforeCheckingAgainAfterAnOutageAndSkipsOnNo(t *testing.T) {
	ts := tailscaleServer(t, http.StatusBadGateway)
	p := &script{confirms: []bool{true, false}}
	f := newFixture(t, nil, nil, p)
	f.env.TailscaleAPI = ts.URL
	vals := map[Slot]Taken{Tailscale: {"tskey-client-kid-0123456789QRST", ".env"}}
	changed, err := Take(context.Background(), f.env, Mapped{}, vals, fillOrder)
	if err != nil || len(changed) != 0 || len(p.confirms) != 0 {
		t.Fatal(changed, err, p.confirms)
	}
	if f.store.m[Tailscale] != "" || f.row("Check Tailscale OAuth client from .env").State != ui.Skipped {
		t.Fatalf("%v %+v", f.store.m, f.rec.Rows())
	}
}
