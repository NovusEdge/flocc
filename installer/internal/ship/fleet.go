package ship

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
)

// Daytona list prices (daytona.io/pricing); the figure shown is an estimate, not what the account
// is billed. The page does not say what a stopped or archived sandbox costs, so those get no
// figure.
const (
	usdPerVCPUHour = 0.0504
	usdPerGiBHour  = 0.0162
	usdPerDiskHour = 0.000108
	freeDiskGiB    = 5

	staleTick   = 10 * time.Minute
	lowDisk     = 0.15
	minInterval = 5 * time.Second
	// The shell convention for a process ended by SIGINT.
	exitInterrupted = 130
	// An SSH token lives 60 minutes; reusing one for less leaves room for a slow ssh.
	tokenReuse = 50 * time.Minute
)

// hostTimeout is a var so a test can shorten it.
var hostTimeout = 20 * time.Second

func estimateHourly(cpu, memGiB, diskGiB int) float64 {
	return float64(cpu)*usdPerVCPUHour + float64(memGiB)*usdPerGiBHour +
		float64(max(diskGiB-freeDiskGiB, 0))*usdPerDiskHour
}

// Report is the JSON of flocc status --json.
type Report struct {
	Host     string `json:"host"`
	Mode     string `json:"mode"`
	LastTick *struct {
		Exit *string  `json:"exit"` // nil while the tick is still running
		AgeS *float64 `json:"age_s"`
	} `json:"last_tick"`
	LedgerError    string  `json:"ledger_error"`
	SessionCap     int     `json:"session_cap"`
	FiveHourMaxPct float64 `json:"five_hour_max_pct"`
	WeeklyMaxPct   float64 `json:"weekly_max_pct"`
	Claims         []Claim `json:"claims"`
	Usage          *struct {
		FiveHourPct float64 `json:"five_hour_pct"`
		WeeklyPct   float64 `json:"weekly_pct"`
	} `json:"usage"`
	Disk *struct {
		FreeBytes  uint64 `json:"free_bytes"`
		TotalBytes uint64 `json:"total_bytes"`
	} `json:"disk"`
	Now         float64           `json:"now"`
	ClaudeLogin *string           `json:"claude_login"` // nil: not asked
	TokenExpiry map[string]string `json:"token_expiry"` // Unix seconds, "never" or "rejected"
}

type Claim struct {
	Issue      string   `json:"issue"`
	State      string   `json:"state"`
	Phase      string   `json:"phase"`
	StartedAge float64  `json:"started_age_s"`
	TmuxAlive  bool     `json:"tmux_alive"`
	ContextPct *float64 `json:"context_pct"`
}

// FleetHost is one row's identity. State is empty for an ssh destination, whose reachability is
// only known by trying it.
type FleetHost struct {
	Name, Target, State  string
	ID                   string // the Daytona sandbox id
	CPU, MemGiB, DiskGiB int
	Daytona              bool
}

type Row struct {
	FleetHost
	Report *Report
	Err    string
}

// discover lists the Daytona org's sandboxes and the ssh hosts in hostsDir. Neither failing stops
// the other: the problems come back as notes for the view to print.
func discover(ctx context.Context, daytonaConfig, hostsDir string, client *http.Client) (found []FleetHost, notes []string) {
	auth, err := loadDaytona(daytonaConfig)
	switch {
	case errors.Is(err, os.ErrNotExist):
		notes = append(notes, "daytona: no CLI config, skipped")
	case err != nil:
		notes = append(notes, "daytona: "+err.Error())
	default:
		sbs, err := auth.sandboxes(ctx, client)
		if err != nil {
			notes = append(notes, "daytona: "+err.Error())
		}
		sort.Slice(sbs, func(i, j int) bool { return sbs[i].Name < sbs[j].Name })
		for _, s := range sbs {
			found = append(found, FleetHost{Name: s.Name, Target: "daytona:" + s.Name, State: strings.ToLower(s.State), ID: s.ID, CPU: s.CPU, MemGiB: s.Memory, DiskGiB: s.Disk, Daytona: true})
		}
	}
	list, err := hosts.List(hostsDir)
	if err != nil {
		for _, line := range strings.Split(err.Error(), "\n") {
			notes = append(notes, "hosts: "+line)
		}
	}
	// Daytona hosts came from the API above.
	for _, h := range list {
		if h.SSH != "" {
			found = append(found, FleetHost{Name: h.Name, Target: h.SSH})
		}
	}
	return found, notes
}

func scrub(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "<token>")
}

// destCache keeps each Daytona host's SSH destination until shortly before its token expires, so
// --watch does not mint a token per host on every redraw.
type destCache struct {
	mu      sync.Mutex
	m       map[string]cachedDest
	now     func() time.Time
	mint    func(context.Context, FleetHost) (Dest, error)
	sshOpts []string // given to every ssh host, which resolve returns without minting
}

type cachedDest struct {
	dest    Dest
	expires time.Time
}

func newDestCache(mint func(context.Context, FleetHost) (Dest, error)) *destCache {
	return &destCache{m: map[string]cachedDest{}, now: time.Now, mint: mint}
}

func (c *destCache) resolve(ctx context.Context, h FleetHost) (Dest, error) {
	if !h.Daytona {
		return Dest{SSH: h.Target, Opts: c.sshOpts}, nil
	}
	c.mu.Lock()
	hit, ok := c.m[h.Target]
	c.mu.Unlock()
	if ok && c.now().Before(hit.expires) {
		return hit.dest, nil
	}
	d, err := c.mint(ctx, h)
	if err != nil {
		return Dest{}, err
	}
	c.mu.Lock()
	c.m[h.Target] = cachedDest{d, c.now().Add(tokenReuse)}
	c.mu.Unlock()
	return d, nil
}

func (c *destCache) forget(h FleetHost) {
	c.mu.Lock()
	delete(c.m, h.Target)
	c.mu.Unlock()
}

// fetch runs flocc status --json on one host. BatchMode keeps ssh from stopping at a
// password prompt, which would hold the view until the timeout.
func fetch(ctx context.Context, r host.Runner, c *destCache, h FleetHost) Row {
	row := Row{FleetHost: h}
	if h.State != "" && h.State != "started" {
		return row
	}
	ctx, cancel := context.WithTimeout(ctx, hostTimeout)
	defer cancel()
	dest, err := c.resolve(ctx, h)
	if err != nil {
		row.Err = firstLine(err.Error())
		return row
	}
	// ssh failing may mean the cached token was revoked, so the next draw mints a fresh one. A
	// remote command that ran and exited non-zero reached the host, so the token still works.
	sshFailed := false
	defer func() {
		if sshFailed {
			c.forget(h)
		}
	}()
	dest.Opts = append(append([]string{}, dest.Opts...), "-o", "BatchMode=yes", "-o", "ConnectTimeout=10")
	res, err := r.Run(ctx, sshCmd(dest, false, asServiceUser([]string{"flocc", "status", "--json"})))
	switch {
	case err != nil:
		sshFailed = true
		row.Err = scrub(firstLine(err.Error()), dest.Token)
	case res.Code != 0:
		sshFailed = res.Code == 255
		row.Err = scrub(fmt.Sprintf("exit %d: %s", res.Code, firstLine(host.Tail(res.Out(), 1))), dest.Token)
	default:
		var rep Report
		if err := json.Unmarshal([]byte(res.Stdout), &rep); err != nil {
			row.Err = "unreadable status output: " + err.Error()
		} else {
			row.Report = &rep
		}
	}
	return row
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// collect queries every host in parallel; one slow or dead host delays only its own row.
func collect(ctx context.Context, r host.Runner, c *destCache, hosts []FleetHost) []Row {
	rows := make([]Row, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows[i] = fetch(ctx, r, c, h)
		}()
	}
	wg.Wait()
	return rows
}

type severity int

const (
	plain severity = iota
	dim
	warn
	bad
)

var sevStyle = map[severity]lipgloss.Style{
	plain: lipgloss.NewStyle(),
	dim:   lipgloss.NewStyle().Faint(true),
	warn:  lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
	bad:   lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
}

func cell(text string, s severity) string { return sevStyle[s].Render(text) }

func age(s float64) string {
	d := time.Duration(s * float64(time.Second))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func pctCell(v, max float64) string {
	s := warn
	if max <= 0 || v < max {
		s = plain
	}
	return cell(fmt.Sprintf("%.0f%%", v), s)
}

func tickCell(rep *Report) string {
	t := rep.LastTick
	if t == nil || t.AgeS == nil {
		return cell("-", dim)
	}
	stale := time.Duration(*t.AgeS*float64(time.Second)) > staleTick
	switch {
	case t.Exit == nil && stale:
		return cell(fmt.Sprintf("%s ago, still running", age(*t.AgeS)), warn)
	case t.Exit == nil:
		return fmt.Sprintf("%s ago, running", age(*t.AgeS))
	case *t.Exit != "0":
		return cell(fmt.Sprintf("%s ago, exit %s", age(*t.AgeS), *t.Exit), bad)
	case stale:
		return cell(fmt.Sprintf("%s ago, ok", age(*t.AgeS)), warn)
	}
	return fmt.Sprintf("%s ago, ok", age(*t.AgeS))
}

func diskCell(rep *Report) string {
	if rep.Disk == nil || rep.Disk.TotalBytes == 0 {
		return cell("-", dim)
	}
	text := fmt.Sprintf("%.1f GiB", float64(rep.Disk.FreeBytes)/(1<<30))
	if float64(rep.Disk.FreeBytes)/float64(rep.Disk.TotalBytes) < lowDisk {
		return cell(text, bad)
	}
	return text
}

// loginCell is bad unless the login is full: only a claude.ai login opens Remote Control.
// "unknown" is a host that could not read its login, which is no evidence of a lapse.
func loginCell(rep *Report) string {
	switch {
	case rep.ClaudeLogin == nil:
		return cell("-", dim)
	case *rep.ClaudeLogin == "full":
		return "full"
	case *rep.ClaudeLogin == "unknown":
		return cell("unknown", warn)
	}
	return cell(*rep.ClaudeLogin, bad)
}

// tokensCell shows the GitHub token that expires first, with the host's own clock from the
// report, at the tick's thresholds: a digest line under 14 days, a page under 3.
func tokensCell(rep *Report) string {
	if len(rep.TokenExpiry) == 0 {
		return cell("-", dim)
	}
	soonest, days := "", math.MaxFloat64
	for _, name := range []string{"agent", "publish"} {
		v, ok := rep.TokenExpiry[name]
		switch {
		case !ok || v == "never":
		case v == "rejected":
			return cell(name+" rejected", bad)
		default:
			at, err := strconv.ParseFloat(v, 64)
			if d := (at - rep.Now) / 86400; err == nil && d < days {
				soonest, days = name, d
			}
		}
	}
	if soonest == "" {
		return "never"
	}
	text := fmt.Sprintf("%s %dd", soonest, int(days))
	switch {
	case days < 3:
		return cell(text, bad)
	case days < 14:
		return cell(text, warn)
	}
	return text
}

func costCell(h FleetHost) string {
	if !h.Daytona || h.State != "started" || h.CPU == 0 && h.MemGiB == 0 {
		return cell("-", dim)
	}
	return fmt.Sprintf("~$%.3f", estimateHourly(h.CPU, h.MemGiB, h.DiskGiB))
}

func newTable(headers ...string) *table.Table {
	return table.New().
		Border(lipgloss.NormalBorder()).
		BorderStyle(sevStyle[dim]).
		Headers(headers...).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == table.HeaderRow {
				return lipgloss.NewStyle().Bold(true).Padding(0, 1)
			}
			return lipgloss.NewStyle().Padding(0, 1)
		})
}

// render draws the host table, the claims table and one line per host that could not be read.
// Ages come from the hosts themselves, so the view needs no clock of its own.
func render(rows []Row, notes []string) string {
	hosts := newTable("HOST", "STATE", "MODE", "LAST TICK", "SESSIONS", "5H", "WEEK", "DISK FREE", "LOGIN", "TOKENS", "EST $/H")
	claims := newTable("HOST", "ISSUE", "PHASE", "STATE", "AGE", "CONTEXT", "TMUX")
	var failures []string
	nClaims := 0
	for _, r := range rows {
		name := r.Name
		if r.Report == nil {
			state := r.State
			sev := dim
			if state == "" || state == "started" {
				state, sev = "unreachable", bad
				failures = append(failures, fmt.Sprintf("%s: %s", name, r.Err))
			}
			d := cell("-", dim)
			hosts.Row(name, cell(state, sev), d, d, d, d, d, d, d, d, costCell(r.FleetHost))
			continue
		}
		rep := r.Report
		state := r.State
		if state == "" {
			state = "up"
		}
		running := 0
		for _, c := range rep.Claims {
			if c.State == "running" {
				running++
			}
		}
		if rep.LedgerError != "" {
			failures = append(failures, fmt.Sprintf("%s: ledger unreadable: %s", name, rep.LedgerError))
		}
		five, week := cell("-", dim), cell("-", dim)
		if rep.Usage != nil {
			five = pctCell(rep.Usage.FiveHourPct, rep.FiveHourMaxPct)
			week = pctCell(rep.Usage.WeeklyPct, rep.WeeklyMaxPct)
		}
		hosts.Row(name, state, rep.Mode, tickCell(rep), fmt.Sprintf("%d/%d", running, rep.SessionCap), five, week, diskCell(rep), loginCell(rep), tokensCell(rep), costCell(r.FleetHost))
		for _, c := range rep.Claims {
			nClaims++
			ctxPct := cell("-", dim)
			if c.ContextPct != nil {
				ctxPct = fmt.Sprintf("%.0f%%", *c.ContextPct)
			}
			// The supervisor kills a blocked claim's session on purpose, so only a running
			// claim with a dead session is wrong.
			tmux := cell("-", dim)
			switch {
			case c.State != "running":
			case c.TmuxAlive:
				tmux = "yes"
			default:
				tmux = cell("no", bad)
			}
			claims.Row(name, c.Issue, c.Phase, c.State, age(c.StartedAge), ctxPct, tmux)
		}
	}
	var b strings.Builder
	b.WriteString(hosts.String() + "\n")
	if nClaims > 0 {
		b.WriteString("\n" + claims.String() + "\n")
	}
	for _, f := range failures {
		b.WriteString(cell(f, bad) + "\n")
	}
	for _, n := range notes {
		b.WriteString(cell(n, dim) + "\n")
	}
	for _, line := range []string{
		"est $/h: an estimate at Daytona list prices, not a bill.",
		"Started sandboxes only: vCPU, memory and disk beyond 5 GiB.",
		"Stopped and archived sandboxes show - (their rate is not confirmed).",
	} {
		b.WriteString(cell(line, dim) + "\n")
	}
	return b.String()
}

// Statuses runs flocc status --json on every host in parallel, as the fleet view does.
func Statuses(ctx context.Context, r host.Runner, hs []hosts.Host) []Row {
	fhs := make([]FleetHost, len(hs))
	for i, h := range hs {
		fhs[i] = FleetHost{Name: h.Name, Target: h.Target(), Daytona: h.Daytona != ""}
	}
	cache := newDestCache(func(ctx context.Context, h FleetHost) (Dest, error) { return Resolve(ctx, h.Target) })
	// No sharing is still a working lookup, so a missing ~/.ssh does not stop it.
	cache.sshOpts, _ = SSHMux()
	return collect(ctx, r, cache, fhs)
}

// FleetMain prints the fleet once, or redraws it every interval with --watch.
func FleetMain(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("flocc status -H all", flag.ContinueOnError)
	watch := fs.Bool("watch", false, "redraw until interrupted")
	interval := fs.Duration("interval", 30*time.Second, "time between redraws with --watch")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *watch && *interval < minInterval {
		fmt.Fprintf(os.Stderr, "fleet: --interval must be at least %s; each redraw calls the Daytona API and every host\n", minInterval)
		return 2
	}
	home, _ := os.UserHomeDir()
	daytonaConfig := filepath.Join(home, ".config/daytona/config.json")
	hostsDir := hosts.Dir(home)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return runFleet(ctx, out, *watch, *interval, daytonaConfig, hostsDir)
}

// runFleet draws the fleet until ctx ends, or once.
func runFleet(ctx context.Context, out io.Writer, watch bool, interval time.Duration, daytonaConfig, hostsDir string) int {
	client := &http.Client{Timeout: 30 * time.Second}
	cache := newDestCache(func(ctx context.Context, h FleetHost) (Dest, error) {
		auth, err := loadDaytona(daytonaConfig)
		if err != nil {
			return Dest{}, err
		}
		return auth.sshDest(ctx, client, h.ID, h.Name, realWaiter(io.Discard))
	})
	cache.sshOpts, _ = SSHMux()
	for {
		found, notes := discover(ctx, daytonaConfig, hostsDir, client)
		rows := collect(ctx, host.Exec{}, cache, found)
		if ctx.Err() != nil {
			return exitInterrupted
		}
		if watch {
			fmt.Fprint(out, "\x1b[H\x1b[2J")
		}
		if len(found) == 0 {
			notes = append(notes, "no hosts: log in with daytona login, or run flocc host add <name>")
		}
		lipgloss.Fprint(out, render(rows, notes))
		if !watch {
			return 0
		}
		select {
		case <-ctx.Done():
			return exitInterrupted
		case <-time.After(interval):
		}
	}
}
