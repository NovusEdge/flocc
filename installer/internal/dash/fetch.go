// Package dash is the full-screen fleet view that bare flocc opens.
package dash

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/ship"
)

// hostTimeout is the host CLI's own status budget. A var so a test can shorten it.
var hostTimeout = 20 * time.Second

// Result is one host's poll. Err is set when no report came; the host's last report is the
// model's to keep.
type Result struct {
	Host   ship.FleetHost
	Status *ship.Report
	Err    error
	At     time.Time
}

// Fetch polls every host in parallel. One that does not answer within hostTimeout gets an error
// result, even when get ignores its context.
func Fetch(ctx context.Context, hs []ship.FleetHost, get func(context.Context, ship.FleetHost) (*ship.Report, error)) []Result {
	out := make([]Result, len(hs))
	var wg sync.WaitGroup
	for i, h := range hs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, hostTimeout)
			defer cancel()
			type answer struct {
				rep *ship.Report
				err error
			}
			ch := make(chan answer, 1)
			go func() {
				rep, err := get(cctx, h)
				ch <- answer{rep, err}
			}()
			r := Result{Host: h}
			select {
			case a := <-ch:
				r.Status, r.Err = a.rep, a.err
			case <-cctx.Done():
				r.Err = errors.New("no answer in " + hostTimeout.String())
				if ctx.Err() != nil {
					r.Err = ctx.Err()
				}
			}
			r.At = time.Now()
			out[i] = r
		}()
	}
	wg.Wait()
	return out
}
