package ship

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/safe"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

// Daytona list prices (daytona.io/pricing); the figure shown is an estimate, not what the account
// is billed. The page does not say what a stopped or archived sandbox costs, so those get no
// figure.
const (
	usdPerVCPUHour = 0.0504
	usdPerGiBHour  = 0.0162
	usdPerDiskHour = 0.000108
	freeDiskGiB    = 5

	staleTick = 10 * time.Minute
	lowDisk   = 0.15
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
	V        int    `json:"v"` // 0 from a host that predates the versioned report
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
	// Absent from a host whose claim has no status file yet.
	UpdatedAge *float64       `json:"updated_age_s"`
	Activity   []ActivityLine `json:"activity"`
}

// ActivityLine is one journal line that names the claim's issue; At is Unix seconds.
type ActivityLine struct {
	At   float64 `json:"at"`
	Text string  `json:"text"`
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
		row.Err = FirstLine(err.Error())
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
		row.Err = scrub(FirstLine(err.Error()), dest.Token)
	case res.Code != 0:
		sshFailed = res.Code == 255
		row.Err = scrub(fmt.Sprintf("exit %d: %s", res.Code, FirstLine(host.Tail(res.Out(), 1))), dest.Token)
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

// FirstLine is s trimmed, up to its first newline.
func FirstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// tracker draws a step per host while collect fetches it: begin opens it, end closes it with the
// row. Nil means no steps.
type tracker struct {
	begin func(FleetHost) *ui.Step
	end   func(*ui.Step, Row)
}

// collect queries every host in parallel; one slow or dead host delays only its own row.
func collect(ctx context.Context, r host.Runner, c *destCache, hosts []FleetHost, t *tracker) []Row {
	rows := make([]Row, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		var st *ui.Step
		if t != nil {
			st = t.begin(h)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows[i] = fetch(ctx, r, c, h)
			if t != nil {
				t.end(st, rows[i])
			}
		}()
	}
	wg.Wait()
	return rows
}

// hostSteps is one step per host named for it, ended Done when its report came, Skipped when it
// did not: an unreachable host is a row of the table, not a failed run.
func hostSteps(s ui.Session) *tracker {
	return &tracker{
		begin: func(h FleetHost) *ui.Step {
			st := s.Step(h.Name)
			st.Detail("status --json")
			return st
		},
		end: func(st *ui.Step, r Row) {
			switch {
			case r.Report != nil && r.Report.V == 0:
				st.Done("behind")
			case r.Report != nil:
				st.Done("up")
			case r.Err != "":
				st.Skip("unreachable: " + safe.Sanitize(r.Err))
			default:
				st.Skip(r.State)
			}
		},
	}
}

// Sev is how a fact reads: the table colours it, the dashboard maps it onto its theme.
type Sev int

const (
	SevPlain Sev = iota
	SevDim
	SevWarn
	SevBad
)

var sevStyle = map[Sev]lipgloss.Style{
	SevPlain: lipgloss.NewStyle(),
	SevDim:   lipgloss.NewStyle().Faint(true),
	SevWarn:  lipgloss.NewStyle().Foreground(lipgloss.Yellow),
	SevBad:   lipgloss.NewStyle().Foreground(lipgloss.Red),
}

func cell(text string, s Sev) string { return sevStyle[s].Render(text) }

func Age(s float64) string {
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

func PctFact(v, max float64) (string, Sev) {
	s := SevWarn
	if max <= 0 || v < max {
		s = SevPlain
	}
	return fmt.Sprintf("%.0f%%", v), s
}

func pctCell(v, max float64) string { return cell(PctFact(v, max)) }

func TickFact(rep *Report) (string, Sev) {
	t := rep.LastTick
	if t == nil || t.AgeS == nil {
		return "-", SevDim
	}
	stale := time.Duration(*t.AgeS*float64(time.Second)) > staleTick
	switch {
	case t.Exit == nil && stale:
		return fmt.Sprintf("%s ago, still running", Age(*t.AgeS)), SevWarn
	case t.Exit == nil:
		return fmt.Sprintf("%s ago, running", Age(*t.AgeS)), SevPlain
	case *t.Exit != "0":
		return fmt.Sprintf("%s ago, exit %s", Age(*t.AgeS), safe.Sanitize(*t.Exit)), SevBad
	case stale:
		return fmt.Sprintf("%s ago, ok", Age(*t.AgeS)), SevWarn
	}
	return fmt.Sprintf("%s ago, ok", Age(*t.AgeS)), SevPlain
}

func tickCell(rep *Report) string { return cell(TickFact(rep)) }

func DiskFact(rep *Report) (string, Sev) {
	if rep.Disk == nil || rep.Disk.TotalBytes == 0 {
		return "-", SevDim
	}
	text := fmt.Sprintf("%.1f GiB", float64(rep.Disk.FreeBytes)/(1<<30))
	if float64(rep.Disk.FreeBytes)/float64(rep.Disk.TotalBytes) < lowDisk {
		return text, SevBad
	}
	return text, SevPlain
}

func diskCell(rep *Report) string { return cell(DiskFact(rep)) }

// LoginFact is bad unless the login is full: only a claude.ai login opens Remote Control.
// "unknown" is a host that could not read its login, which is no evidence of a lapse.
func LoginFact(rep *Report) (string, Sev) {
	switch {
	case rep.ClaudeLogin == nil:
		return "-", SevDim
	case *rep.ClaudeLogin == "full":
		return "full", SevPlain
	case *rep.ClaudeLogin == "unknown":
		return "unknown", SevWarn
	}
	return safe.Sanitize(*rep.ClaudeLogin), SevBad
}

func loginCell(rep *Report) string { return cell(LoginFact(rep)) }

// TokensFact shows the GitHub token that expires first, with the host's own clock from the
// report, at the tick's thresholds: a digest line under 14 days, a page under 3.
func TokensFact(rep *Report) (string, Sev) {
	if len(rep.TokenExpiry) == 0 {
		return "-", SevDim
	}
	soonest, days := "", math.MaxFloat64
	for _, name := range []string{"agent", "publish"} {
		v, ok := rep.TokenExpiry[name]
		switch {
		case !ok || v == "never":
		case v == "rejected":
			return name + " rejected", SevBad
		default:
			at, err := strconv.ParseFloat(v, 64)
			if d := (at - rep.Now) / 86400; err == nil && d < days {
				soonest, days = name, d
			}
		}
	}
	if soonest == "" {
		return "never", SevPlain
	}
	text := fmt.Sprintf("%s %dd", soonest, int(days))
	switch {
	case days < 3:
		return text, SevBad
	case days < 14:
		return text, SevWarn
	}
	return text, SevPlain
}

func tokensCell(rep *Report) string { return cell(TokensFact(rep)) }

func costCell(h FleetHost) string {
	if !h.Daytona || h.State != "started" || h.CPU == 0 && h.MemGiB == 0 {
		return cell("-", SevDim)
	}
	return fmt.Sprintf("~$%.3f", estimateHourly(h.CPU, h.MemGiB, h.DiskGiB))
}

func newTable(headers ...string) *table.Table {
	return table.New().
		Border(lipgloss.NormalBorder()).
		BorderStyle(sevStyle[SevDim]).
		Headers(headers...).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == table.HeaderRow {
				return lipgloss.NewStyle().Bold(true).Padding(0, 1)
			}
			return lipgloss.NewStyle().Padding(0, 1)
		})
}

// fleetTables builds the host table and the claims table (when anything is claimed), with one line
// for each thing that is wrong. Cells carry lipgloss styling for render; Fleet strips it. Ages come
// from the hosts themselves, so the view needs no clock of its own.
func fleetTables(rows []Row) (tables []ui.Table, failures []string) {
	hosts := ui.Table{Title: "Hosts", Headers: []string{"HOST", "STATE", "MODE", "LAST TICK", "SESSIONS", "5H", "WEEK", "DISK FREE", "LOGIN", "TOKENS", "EST $/H"}}
	claims := ui.Table{Title: "Claims", Headers: []string{"HOST", "ISSUE", "PHASE", "STATE", "AGE", "CONTEXT", "TMUX"}}
	for _, r := range rows {
		name := r.Name
		if r.Report == nil {
			state := r.State
			sev := SevDim
			if state == "" || state == "started" {
				state, sev = "unreachable", SevBad
				failures = append(failures, fmt.Sprintf("%s: %s", name, safe.Sanitize(r.Err)))
			}
			d := cell("-", SevDim)
			hosts.Rows = append(hosts.Rows, []string{name, cell(state, sev), d, d, d, d, d, d, d, d, costCell(r.FleetHost)})
			continue
		}
		rep := r.Report
		state := r.State
		if state == "" {
			state = "up"
		}
		if rep.V == 0 {
			state = cell("behind", SevWarn)
		}
		running := 0
		for _, c := range rep.Claims {
			if c.State == "running" {
				running++
			}
		}
		if rep.LedgerError != "" {
			failures = append(failures, fmt.Sprintf("%s: ledger unreadable: %s", name, safe.Sanitize(rep.LedgerError)))
		}
		five, week := cell("-", SevDim), cell("-", SevDim)
		if rep.Usage != nil {
			five = pctCell(rep.Usage.FiveHourPct, rep.FiveHourMaxPct)
			week = pctCell(rep.Usage.WeeklyPct, rep.WeeklyMaxPct)
		}
		hosts.Rows = append(hosts.Rows, []string{name, state, rep.Mode, tickCell(rep), fmt.Sprintf("%d/%d", running, rep.SessionCap), five, week, diskCell(rep), loginCell(rep), tokensCell(rep), costCell(r.FleetHost)})
		for _, c := range rep.Claims {
			ctxPct := cell("-", SevDim)
			if c.ContextPct != nil {
				ctxPct = fmt.Sprintf("%.0f%%", *c.ContextPct)
			}
			// The supervisor kills a blocked claim's session on purpose, so only a running
			// claim with a dead session is wrong.
			tmux := cell("-", SevDim)
			switch {
			case c.State != "running":
			case c.TmuxAlive:
				tmux = "yes"
			default:
				tmux = cell("no", SevBad)
			}
			claims.Rows = append(claims.Rows, []string{name, c.Issue, c.Phase, c.State, Age(c.StartedAge), ctxPct, tmux})
		}
	}
	tables = append(tables, hosts)
	if len(claims.Rows) > 0 {
		tables = append(tables, claims)
	}
	return tables, failures
}

// footnotes explain the cost column.
var footnotes = []string{
	"est $/h: an estimate at Daytona list prices, not a bill.",
	"Started sandboxes only: vCPU, memory and disk beyond 5 GiB.",
	"Stopped and archived sandboxes show - (their rate is not confirmed).",
}

// unstyled drops the lipgloss styling of a cell.
func unstyled(s string) string {
	var b bytes.Buffer
	lipgloss.Fprint(&b, s)
	return b.String()
}

// draw puts the fleet on the session: the tables, then the problems and the notes.
func draw(s ui.Session, rows []Row, notes []string) {
	tables, failures := fleetTables(rows)
	for _, t := range tables {
		for _, r := range t.Rows {
			for j, c := range r {
				r[j] = unstyled(c)
			}
		}
		s.Table(t)
	}
	if len(failures) > 0 {
		s.Note("Problems", strings.Join(failures, "\n"))
	}
	s.Note("Notes", strings.Join(append(slices.Clone(notes), footnotes...), "\n"))
}

// Statuses runs flocc status --json on every host in parallel, as the fleet view does, with a
// "Find <issue>" step per host on s.
func Statuses(ctx context.Context, s ui.Session, r host.Runner, hs []hosts.Host, issue string) []Row {
	fhs := make([]FleetHost, len(hs))
	for i, h := range hs {
		fhs[i] = FleetHost{Name: h.Name, Target: h.Target(), Daytona: h.Daytona != ""}
	}
	cache := newDestCache(func(ctx context.Context, h FleetHost) (Dest, error) { return Resolve(ctx, h.Target) })
	// No sharing is still a working lookup, so a missing ~/.ssh does not stop it.
	cache.sshOpts, _ = SSHMux()
	return collect(ctx, r, cache, fhs, &tracker{
		begin: func(h FleetHost) *ui.Step {
			st := s.Step("Find " + issue)
			st.Detail(h.Name)
			return st
		},
		end: func(st *ui.Step, row Row) {
			switch {
			case row.Report == nil:
				st.Skip(row.Name + ": unreachable")
			case slices.ContainsFunc(row.Report.Claims, func(c Claim) bool { return c.Issue == issue }):
				st.Done(row.Name + ": holds it")
			default:
				st.Done(row.Name + ": not here")
			}
		},
	})
}

// Fleet draws the fleet once on s.
func Fleet(ctx context.Context, s ui.Session, args []string) int {
	fs := flag.NewFlagSet("flocc status -H all", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return 2
	}
	home, _ := os.UserHomeDir()
	return runFleet(ctx, s, filepath.Join(home, ".config/daytona/config.json"), hosts.Dir(home))
}

// Source is the fleet as the desktop sees it: the hosts it knows and a cache of the ssh
// destinations it minted for them, kept between polls.
type Source struct {
	daytonaConfig, hostsDir string
	client                  *http.Client
	cache                   *destCache
	run                     host.Runner
}

func newSource(daytonaConfig, hostsDir string) *Source {
	s := &Source{daytonaConfig: daytonaConfig, hostsDir: hostsDir,
		client: &http.Client{Timeout: 30 * time.Second}, run: host.Exec{}}
	s.cache = newDestCache(func(ctx context.Context, h FleetHost) (Dest, error) {
		auth, err := loadDaytona(daytonaConfig)
		if err != nil {
			return Dest{}, err
		}
		return auth.sshDest(ctx, s.client, h.ID, h.Name, realWaiter(io.Discard))
	})
	s.cache.sshOpts, _ = SSHMux()
	return s
}

// NewSource reads the same places `flocc status -H all` does.
func NewSource() *Source {
	home, _ := os.UserHomeDir()
	return newSource(filepath.Join(home, ".config/daytona/config.json"), hosts.Dir(home))
}

func (s *Source) Discover(ctx context.Context) ([]FleetHost, []string) {
	return discover(ctx, s.daytonaConfig, s.hostsDir, s.client)
}

// Status is flocc status --json on one host. A host that is not started has neither a report nor
// an error from fetch, so its state becomes the error.
func (s *Source) Status(ctx context.Context, h FleetHost) (*Report, error) {
	row := fetch(ctx, s.run, s.cache, h)
	switch {
	case row.Report != nil:
		return row.Report, nil
	case row.Err != "":
		return nil, errors.New(row.Err)
	}
	return nil, errors.New(h.State)
}

// DoctorReport is flocc doctor --json; V is 0 from a host that predates it.
type DoctorReport struct {
	V      int           `json:"v"`
	Checks []DoctorCheck `json:"checks"`
}

type DoctorCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Warn   bool   `json:"warn"`
	Detail string `json:"detail"`
}

// Run runs the host CLI with args on h as the service user, without a terminal, within
// hostTimeout. stdout and stderr come back scrubbed of h's token.
func (s *Source) Run(ctx context.Context, h FleetHost, args []string) (host.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, hostTimeout)
	defer cancel()
	dest, err := s.cache.resolve(ctx, h)
	if err != nil {
		return host.Result{}, err
	}
	dest.Opts = append(slices.Clone(dest.Opts), "-o", "ConnectTimeout=10")
	res, err := Capture(ctx, s.run, dest, FloccLine(args), nil)
	// As in fetch: only ssh failing says the token may be revoked.
	if err != nil || res.Code == 255 {
		s.cache.forget(h)
	}
	return res, err
}

// runFleet draws the fleet once.
func runFleet(ctx context.Context, s ui.Session, daytonaConfig, hostsDir string) int {
	src := newSource(daytonaConfig, hostsDir)
	found, notes := src.Discover(ctx)
	rows := collect(ctx, src.run, src.cache, found, hostSteps(s))
	if ctx.Err() != nil {
		return exitInterrupted
	}
	if len(found) == 0 {
		notes = append(notes, "no hosts: log in with daytona login, or run flocc host add <name>")
	}
	draw(s, rows, notes)
	return 0
}
