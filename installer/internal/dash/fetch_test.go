package dash

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/ship"
)

func TestFetchTimesOutASlowHostWithoutHoldingTheOthers(t *testing.T) {
	old := hostTimeout
	hostTimeout = 50 * time.Millisecond
	defer func() { hostTimeout = old }()
	release := make(chan struct{})
	defer close(release)

	hs := []ship.FleetHost{{Name: "slow"}, {Name: "fast"}, {Name: "down"}}
	got := Fetch(context.Background(), hs, func(_ context.Context, h ship.FleetHost) (*ship.Report, error) {
		switch h.Name {
		case "slow":
			<-release // ignores its context, as a wedged ssh would
		case "down":
			return nil, errors.New("exit 255")
		}
		return &ship.Report{V: 1}, nil
	})
	if got[0].Host.Name != "slow" || got[0].Err == nil || !strings.Contains(got[0].Err.Error(), "no answer") || got[0].Status != nil {
		t.Errorf("slow: %+v", got[0])
	}
	if got[1].Err != nil || got[1].Status == nil || got[1].At.IsZero() {
		t.Errorf("fast: %+v", got[1])
	}
	if got[2].Err == nil || got[2].Err.Error() != "exit 255" {
		t.Errorf("down: %+v", got[2])
	}
}

func TestFetchReportsACancelledRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := Fetch(ctx, []ship.FleetHost{{Name: "a"}}, func(ctx context.Context, _ ship.FleetHost) (*ship.Report, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if !errors.Is(got[0].Err, context.Canceled) {
		t.Errorf("%+v", got[0])
	}
}
