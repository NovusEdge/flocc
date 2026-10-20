package create

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type Incus struct{}

func (Incus) Name() string { return "Incus" }

type incusReport struct {
	ok          bool
	detail, fix string
}

// incusLine is an incus command printed for the owner to type. Without sudo it would fail on the
// socket, or for a restricted incus group member act on that user's own project, not the shared one.
func (e *Env) incusLine(args string) string {
	if e.incusSudo {
		return "sudo incus " + args
	}
	return "incus " + args
}

func inspectIncus(ctx context.Context, e *Env) (incusReport, error) {
	var nw struct {
		Managed bool   `json:"managed"`
		Type    string `json:"type"`
	}
	if json.Unmarshal([]byte(e.out(ctx, e.incus("query", "/1.0/networks/"+Bridge))), &nw) != nil || !nw.Managed || nw.Type != "bridge" {
		return incusReport{detail: "no managed bridge " + Bridge,
			fix: e.incusLine("admin init --minimal") + "     (a fresh Incus: it makes " + Bridge + " and a default pool)\n  " +
				e.incusLine("network create "+Bridge) + "     (an Incus already set up without it)"}, nil
	}
	if e.ok(ctx, e.incus("query", "/1.0/instances/"+e.Spec.Name)) {
		// Room was checked when the VM was made; a re-run must not refuse the VM for using it.
		return incusReport{ok: true, detail: Bridge + "; " + e.Spec.Name + " exists"}, nil
	}
	pool := e.out(ctx, e.incus("profile", "device", "get", "default", "root", "pool"))
	if pool == "" {
		return incusReport{detail: "the default profile has no root disk",
			fix: e.incusLine("storage create default dir") + "\n  " + e.incusLine("profile device add default root disk path=/ pool=default")}, nil
	}
	var res struct {
		Space struct{ Used, Total uint64 } `json:"space"`
	}
	if err := json.Unmarshal([]byte(e.out(ctx, e.incus("query", "/1.0/storage-pools/"+pool+"/resources"))), &res); err != nil {
		return incusReport{}, fmt.Errorf("reading storage pool %s: %w", pool, err)
	}
	var free uint64
	if res.Space.Total > res.Space.Used {
		free = res.Space.Total - res.Space.Used
	}
	if free < uint64(e.Spec.DiskGiB)<<30 {
		return incusReport{detail: fmt.Sprintf("pool %s has %s free; the VM needs %d GiB", pool, gib(free), e.Spec.DiskGiB),
			fix: "free space in pool " + pool + ", or run create with a smaller --disk"}, nil
	}
	avail, err := memAvailable(e.out(ctx, cmd("cat", "/proc/meminfo")))
	if err != nil {
		return incusReport{}, err
	}
	if avail < uint64(e.Spec.MemoryGiB)<<30 {
		return incusReport{detail: fmt.Sprintf("%s of memory available; the VM needs %d GiB", gib(avail), e.Spec.MemoryGiB),
			fix: "stop another VM (" + e.incusLine("stop <name>") + "), or run create with a smaller --memory"}, nil
	}
	return incusReport{ok: true, detail: fmt.Sprintf("%s; pool %s %s free; %s of memory available", Bridge, pool, gib(free), gib(avail))}, nil
}

func (Incus) Check(ctx context.Context, e *Env) (bool, string, error) {
	r, err := inspectIncus(ctx, e)
	return r.ok, r.detail, err
}

// Apply never sets Incus up itself: the pool and the network are the owner's choice on a
// machine shared with other work.
func (Incus) Apply(ctx context.Context, e *Env) error {
	r, err := inspectIncus(ctx, e)
	if err != nil {
		return err
	}
	return fmt.Errorf("%s. On %s run:\n  %s\nthen run create again", r.detail, e.Spec.Machine, r.fix)
}

func memAvailable(meminfo string) (uint64, error) {
	for _, line := range strings.Split(meminfo, "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && f[0] == "MemAvailable:" && f[2] == "kB" {
			kb, err := strconv.ParseUint(f[1], 10, 64)
			return kb << 10, err
		}
	}
	return 0, errors.New("/proc/meminfo has no MemAvailable line")
}

func gib(b uint64) string { return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30)) }
