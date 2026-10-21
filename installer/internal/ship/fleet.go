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

	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/steps"
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
	UpdatedAge  *float64       `json:"updated_age_s"`
	Launches    int            `json:"launches"`
	LaunchLimit int            `json:"launch_limit"`
	Activity    []ActivityLine `json:"activity"`
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

// CardinalRow is one cardinal install in the fleet view; Status is nil when it could not be read.
type CardinalRow struct {
	Saved  saved.Saved
	Status *steps.CardinalStatus
	Err    string
}

// fetchCardinal runs the quick status as the cardinal user. Its binary is cardinal's to rewrite,
// so the output is only parsed here and sanitized where render prints it.
func fetchCardinal(ctx context.Context, r host.Runner, mux []string, s saved.Saved) CardinalRow {
	row := CardinalRow{Saved: s}
	ctx, cancel := context.WithTimeout(ctx, hostTimeout)
	defer cancel()
	args := append(append(append([]string{}, saved.Pinned...), mux...), "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		s.Dest(), "~/.local/lib/flocc-cardinal/flocc-setup --phase cardinal-status --quick")
	res, err := r.Run(ctx, host.Cmd{Name: "ssh", Args: args})
	var st steps.CardinalStatus
	switch {
	case err != nil:
		row.Err = firstLine(err.Error())
	case res.Code != 0:
		row.Err = fmt.Sprintf("exit %d: %s", res.Code, firstLine(host.Tail(res.Out(), 1)))
	case json.Unmarshal([]byte(res.Stdout), &st) != nil:
		row.Err = "unreadable status output"
	case st.Schema != steps.CardinalStatusSchema:
		row.Err = fmt.Sprintf("status schema %d is not one this desktop reads; run flocc cardinal setup --user %s", st.Schema, s.User)
	default:
		row.Status = &st
	}
	return row
}

func collectCardinals(ctx context.Context, r host.Runner, mux []string, list []saved.Saved) []CardinalRow {
	rows := make([]CardinalRow, len(list))
	var wg sync.WaitGroup
	for i, s := range list {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows[i] = fetchCardinal(ctx, r, mux, s)
		}()
	}
	wg.Wait()
	return rows
}

// discoverCardinals is every saved install that remove has not taken down.
func discoverCardinals(dir string) (list []saved.Saved, notes []string) {
	all, err := saved.List(dir)
	if err != nil {
		for _, line := range strings.Split(err.Error(), "\n") {
			notes = append(notes, "cardinal: "+line)
		}
	}
	for _, s := range all {
		if !s.Removed {
			list = append(list, s)
		}
	}
	return list, notes
}

// cardinalTable lists the CARDINAL rows and adds a failure line for each install that fails.
func cardinalTable(rows []CardinalRow) (ui.Table, []string) {
	t := ui.Table{Headers: []string{"CARDINAL", "MACHINE", "UNIT", "SESSION", "LAST TURN", "LOGIN"}}
	add := func(cells ...string) { t.Rows = append(t.Rows, cells) }
	var failures []string
	for _, r := range rows {
		user, machine := r.Saved.User, saved.HostPart(r.Saved.Machine)
		remedy := "flocc cardinal status --user " + user
		if r.Status == nil {
			d := cell("-", dim)
			add(user, machine, cell("unreachable", bad), d, d, d)
			failures = append(failures, fmt.Sprintf("cardinal %s: %s; %s", user, saved.Sanitize(r.Err), remedy))
			continue
		}
		st := r.Status
		unit := saved.Sanitize(st.Unit)
		if st.Unit != "active" {
			unit = cell(unit, bad)
		}
		login := saved.Sanitize(st.Login)
		if st.Login != "full" {
			login = cell(login, bad)
		}
		session, turn := cell("-", dim), cell("-", dim)
		if st.SessionAgeS != nil {
			session = Age(*st.SessionAgeS)
		}
		if st.LastTurnAgeS != nil {
			turn = Age(*st.LastTurnAgeS) + " ago"
		}
		add(user, machine, unit, session, turn, login)
		if f := st.Failures(); len(f) > 0 {
			failures = append(failures, fmt.Sprintf("cardinal %s: %s; %s", user, saved.Sanitize(f[0]), remedy))
		}
	}
	return t, failures
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
				st.Skip("unreachable: " + saved.Sanitize(r.Err))
			default:
				st.Skip(r.State)
			}
		},
	}
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
	warn:  lipgloss.NewStyle().Foreground(lipgloss.Yellow),
	bad:   lipgloss.NewStyle().Foreground(lipgloss.Red),
}

func cell(text string, s severity) string { return sevStyle[s].Render(text) }

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
		return cell(fmt.Sprintf("%s ago, still running", Age(*t.AgeS)), warn)
	case t.Exit == nil:
		return fmt.Sprintf("%s ago, running", Age(*t.AgeS))
	case *t.Exit != "0":
		return cell(fmt.Sprintf("%s ago, exit %s", Age(*t.AgeS), *t.Exit), bad)
	case stale:
		return cell(fmt.Sprintf("%s ago, ok", Age(*t.AgeS)), warn)
	}
	return fmt.Sprintf("%s ago, ok", Age(*t.AgeS))
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

// fleetTables builds the host table, the claims table (when anything is claimed) and the cardinal
// table (when any install is saved), with one line for each thing that is wrong. Cells carry
// lipgloss styling for render; Fleet strips it. Ages come from the hosts themselves, so the view
// needs no clock of its own.
func fleetTables(rows []Row, cards []CardinalRow) (tables []ui.Table, failures []string) {
	hosts := ui.Table{Title: "Hosts", Headers: []string{"HOST", "STATE", "MODE", "LAST TICK", "SESSIONS", "5H", "WEEK", "DISK FREE", "LOGIN", "TOKENS", "EST $/H"}}
	claims := ui.Table{Title: "Claims", Headers: []string{"HOST", "ISSUE", "PHASE", "STATE", "AGE", "CONTEXT", "TMUX"}}
	for _, r := range rows {
		name := r.Name
		if r.Report == nil {
			state := r.State
			sev := dim
			if state == "" || state == "started" {
				state, sev = "unreachable", bad
				failures = append(failures, fmt.Sprintf("%s: %s", name, saved.Sanitize(r.Err)))
			}
			d := cell("-", dim)
			hosts.Rows = append(hosts.Rows, []string{name, cell(state, sev), d, d, d, d, d, d, d, d, costCell(r.FleetHost)})
			continue
		}
		rep := r.Report
		state := r.State
		if state == "" {
			state = "up"
		}
		if rep.V == 0 {
			state = cell("behind", warn)
		}
		running := 0
		for _, c := range rep.Claims {
			if c.State == "running" {
				running++
			}
		}
		if rep.LedgerError != "" {
			failures = append(failures, fmt.Sprintf("%s: ledger unreadable: %s", name, saved.Sanitize(rep.LedgerError)))
		}
		five, week := cell("-", dim), cell("-", dim)
		if rep.Usage != nil {
			five = pctCell(rep.Usage.FiveHourPct, rep.FiveHourMaxPct)
			week = pctCell(rep.Usage.WeeklyPct, rep.WeeklyMaxPct)
		}
		hosts.Rows = append(hosts.Rows, []string{name, state, rep.Mode, tickCell(rep), fmt.Sprintf("%d/%d", running, rep.SessionCap), five, week, diskCell(rep), loginCell(rep), tokensCell(rep), costCell(r.FleetHost)})
		for _, c := range rep.Claims {
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
			claims.Rows = append(claims.Rows, []string{name, c.Issue, c.Phase, c.State, Age(c.StartedAge), ctxPct, tmux})
		}
	}
	tables = append(tables, hosts)
	if len(claims.Rows) > 0 {
		tables = append(tables, claims)
	}
	if len(cards) > 0 {
		t, fails := cardinalTable(cards)
		tables = append(tables, t)
		failures = append(failures, fails...)
	}
	return tables, failures
}

// footnotes explain the cost column.
var footnotes = []string{
	"est $/h: an estimate at Daytona list prices, not a bill.",
	"Started sandboxes only: vCPU, memory and disk beyond 5 GiB.",
	"Stopped and archived sandboxes show - (their rate is not confirmed).",
}

// render draws the fleet as text for --watch, which clears the screen and redraws.
func render(rows []Row, cards []CardinalRow, notes []string) string {
	tables, failures := fleetTables(rows, cards)
	var b strings.Builder
	for i, t := range tables {
		lt := newTable(t.Headers...)
		for _, r := range t.Rows {
			lt.Row(r...)
		}
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(lt.String() + "\n")
	}
	for _, f := range failures {
		b.WriteString(cell(f, bad) + "\n")
	}
	for _, n := range notes {
		b.WriteString(cell(n, dim) + "\n")
	}
	for _, line := range footnotes {
		b.WriteString(cell(line, dim) + "\n")
	}
	return b.String()
}

// unstyled drops the lipgloss styling of a cell.
func unstyled(s string) string {
	var b bytes.Buffer
	lipgloss.Fprint(&b, s)
	return b.String()
}

// draw puts the fleet on the session: the tables, then the problems and the notes.
func draw(s ui.Session, rows []Row, cards []CardinalRow, notes []string) {
	tables, failures := fleetTables(rows, cards)
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

// Fleet draws the fleet once on s, or with --watch redraws it on out every interval. The watch
// loop owns the terminal, so run it without a live session view.
func Fleet(ctx context.Context, s ui.Session, out io.Writer, args []string) int {
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
	return runFleet(ctx, s, out, *watch, *interval, daytonaConfig, hostsDir, saved.Dir(home))
}

// Source is the fleet as the desktop sees it: the hosts it knows and a cache of the ssh
// destinations it minted for them, kept between polls.
type Source struct {
	daytonaConfig, hostsDir, cardinalDir string
	client                               *http.Client
	cache                                *destCache
}

func newSource(daytonaConfig, hostsDir, cardinalDir string) *Source {
	s := &Source{daytonaConfig: daytonaConfig, hostsDir: hostsDir, cardinalDir: cardinalDir,
		client: &http.Client{Timeout: 30 * time.Second}}
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
	return newSource(filepath.Join(home, ".config/daytona/config.json"), hosts.Dir(home), saved.Dir(home))
}

func (s *Source) Discover(ctx context.Context) ([]FleetHost, []string) {
	return discover(ctx, s.daytonaConfig, s.hostsDir, s.client)
}

// Status is flocc status --json on one host. A host that is not started has neither a report nor
// an error from fetch, so its state becomes the error.
func (s *Source) Status(ctx context.Context, h FleetHost) (*Report, error) {
	row := fetch(ctx, host.Exec{}, s.cache, h)
	switch {
	case row.Report != nil:
		return row.Report, nil
	case row.Err != "":
		return nil, errors.New(row.Err)
	}
	return nil, errors.New(h.State)
}

// Cardinals runs the quick status of every saved install that is not removed.
func (s *Source) Cardinals(ctx context.Context) ([]CardinalRow, []string) {
	list, notes := discoverCardinals(s.cardinalDir)
	return collectCardinals(ctx, host.Exec{}, s.cache.sshOpts, list), notes
}

// runFleet draws the fleet until ctx ends, or once.
func runFleet(ctx context.Context, s ui.Session, out io.Writer, watch bool, interval time.Duration, daytonaConfig, hostsDir, cardinalDir string) int {
	src := newSource(daytonaConfig, hostsDir, cardinalDir)
	var track *tracker
	if !watch {
		track = hostSteps(s)
	}
	for {
		found, notes := src.Discover(ctx)
		cardList, cardNotes := discoverCardinals(cardinalDir)
		notes = append(notes, cardNotes...)
		var cards []CardinalRow
		done := make(chan struct{})
		go func() {
			cards = collectCardinals(ctx, host.Exec{}, src.cache.sshOpts, cardList)
			close(done)
		}()
		rows := collect(ctx, host.Exec{}, src.cache, found, track)
		<-done
		if ctx.Err() != nil {
			return exitInterrupted
		}
		if len(found) == 0 {
			notes = append(notes, "no hosts: log in with daytona login, or run flocc host add <name>")
		}
		if !watch {
			draw(s, rows, cards, notes)
			return 0
		}
		fmt.Fprint(out, "\x1b[H\x1b[2J")
		lipgloss.Fprint(out, render(rows, cards, notes))
		select {
		case <-ctx.Done():
			return exitInterrupted
		case <-time.After(interval):
		}
	}
}
