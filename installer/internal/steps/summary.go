package steps

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

// SummaryMarker starts the one line that ends the service and login phases' output. The
// desktop takes that line out of the stream and prints the summary itself.
const SummaryMarker = "flocc-summary "

// Summary is what the service and login phases report at the end.
type Summary struct {
	Version      string               `json:"version"`
	Login        string               `json:"login"` // full, token-only (<method>) or none
	LoginPending bool                 `json:"login_pending"`
	TokenExpiry  map[string]time.Time `json:"token_expiry,omitempty"` // by "agent token" / "publish token"; the zero time never expires
	Failed       []string             `json:"failed,omitempty"`       // doctor FAIL lines, a pending Claude login's left out
	Timer        bool                 `json:"timer"`
	Sessions     []string             `json:"sessions,omitempty"` // live sessions, "NF-1 impl"
	SessionsErr  string               `json:"sessions_err,omitempty"`
	// Target is the desktop's name for the host, for the login command Print shows.
	Target string `json:"-"`
}

// now is a variable so tests can fix the days left on a token.
var now = time.Now

func (s Summary) JSON() string {
	b, _ := json.Marshal(s) // plain strings, bools and times cannot fail
	return string(b)
}

func (s Summary) Line() string { return SummaryMarker + s.JSON() }

func ParseSummaryLine(line string) (Summary, bool) {
	var s Summary
	rest, ok := strings.CutPrefix(line, SummaryMarker)
	if !ok || json.Unmarshal([]byte(rest), &s) != nil {
		return Summary{}, false
	}
	return s, true
}

// Redact returns a copy with each secret replaced by *** in every free-text field. The desktop
// prints Line to its own stdout, outside host.Exec's redaction, and Failed and Version carry raw
// host output.
func (s Summary) Redact(secrets ...string) Summary {
	// strings.Replacer prefers the earlier pair at a position, so longest first keeps a secret
	// that contains another from leaking its tail.
	secrets = slices.DeleteFunc(slices.Clone(secrets), func(x string) bool { return x == "" })
	slices.SortStableFunc(secrets, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	var pairs []string
	for _, x := range secrets {
		pairs = append(pairs, x, "***")
	}
	r := strings.NewReplacer(pairs...)
	all := func(in []string) []string {
		var out []string
		for _, v := range in {
			out = append(out, r.Replace(v))
		}
		return out
	}
	s.Version, s.Login, s.SessionsErr = r.Replace(s.Version), r.Replace(s.Login), r.Replace(s.SessionsErr)
	s.Failed, s.Sessions = all(s.Failed), all(s.Sessions)
	return s
}

type hostStatus struct {
	TickLock    string `json:"tick_lock"`
	LedgerError string `json:"ledger_error"`
	Claims      []struct {
		Issue     string `json:"issue"`
		Phase     string `json:"phase"`
		TmuxAlive bool   `json:"tmux_alive"`
	} `json:"claims"`
}

func (s hostStatus) live() []string {
	var live []string
	for _, c := range s.Claims {
		if c.TmuxAlive {
			live = append(live, c.Issue+" "+c.Phase)
		}
	}
	return live
}

// readStatus asks flocc status --json, which takes no lock and calls neither Linear nor GitHub.
func readStatus(ctx context.Context, e *Env) (hostStatus, error) {
	var st hostStatus
	out, err := e.MustCmd(ctx, host.Cmd{Name: e.Real(".local/bin/flocc"), Args: []string{"status", "--json"},
		Env: []string{ServicePath(e.Home)}})
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal([]byte(out), &st)
}

// doctorLine splits a line of flocc doctor's report: a status padded to 6 columns, then
// "name: detail".
func doctorLine(line string) (name, detail string, ok bool) {
	if len(line) < 7 {
		return "", "", false
	}
	return strings.Cut(line[6:], ": ")
}

// tokenDetail picks the lifetime out of a doctor line's detail; a rejected token is a FAIL line
// and shows under Failed instead.
var tokenDetail = regexp.MustCompile(`^(?:expires (\d{4}-\d{2}-\d{2})|never expires)`)

func Summarize(ctx context.Context, e *Env) Summary {
	d := e.lastDoctor
	if d == nil {
		out, fails, ok := doctor(ctx, e)
		d = &doctorResult{out: out, fails: fails, ok: ok}
	}
	s := Summary{
		Version: e.Output(ctx, e.Real(".local/bin/flocc"), "--version"),
		Login:   claudeLogin(ctx, e),
		Failed:  d.fails,
		Timer:   timerOn(ctx, e),
	}
	s.LoginPending = s.Login != "full"
	// With a full login, doctor's login FAIL (a probe timeout, say) kept the timer off.
	if s.LoginPending {
		s.Failed = withoutLogin(d.fails)
	}
	if !d.ok && len(d.fails) == 0 {
		s.Failed = []string{"FAIL  doctor: did not run cleanly; run flocc doctor on the host"}
	}
	for _, line := range strings.Split(d.out, "\n") {
		name, detail, ok := doctorLine(line)
		m := tokenDetail.FindStringSubmatch(detail)
		if !ok || !strings.HasPrefix(name, "github ") || m == nil {
			continue
		}
		var exp time.Time // never expires
		if m[1] != "" {
			exp, _ = time.Parse("2006-01-02", m[1])
		}
		if s.TokenExpiry == nil {
			s.TokenExpiry = map[string]time.Time{}
		}
		s.TokenExpiry[strings.TrimPrefix(name, "github ")] = exp
	}
	if st, err := readStatus(ctx, e); err != nil {
		s.SessionsErr = "flocc status failed"
	} else {
		s.Sessions = st.live()
	}
	return s
}

// Failing reports a FAIL line other than a pending Claude login's, which flocc login fixes.
func (s Summary) Failing() bool { return len(s.Failed) > 0 }

// Rows is the summary the owner reads, as ended steps; Target is the desktop's name for the host.
// A login still to do, a timer that waits for it and an unreadable session list end Skipped, not
// Pending: a host with only its Claude login open exits 0, and Pending would make the session exit 1.
func (s Summary) Rows() []ui.Event {
	if s.Login == "" {
		return nil
	}
	row := func(name string, state ui.State, detail string) ui.Event {
		return ui.Event{Kind: ui.KStep, Name: name, State: state, Detail: detail}
	}
	rows := []ui.Event{row("version", ui.Done, cmp.Or(s.Version, "flocc version unknown"))}
	if s.LoginPending {
		rows = append(rows, row("claude login", ui.Skipped, s.Login+"; on the desktop run: flocc login -H "+cmp.Or(s.Target, "<host>")))
	} else {
		rows = append(rows, row("claude login", ui.Done, s.Login))
	}
	for _, name := range slices.Sorted(maps.Keys(s.TokenExpiry)) {
		exp := s.TokenExpiry[name]
		switch days := int(exp.Sub(now()).Hours() / 24); {
		case exp.IsZero():
			rows = append(rows, row(name, ui.Done, "never expires"))
		case days < 0:
			rows = append(rows, row(name, ui.Pending, "expired "+exp.Format("2006-01-02")))
		default:
			rows = append(rows, row(name, ui.Done, fmt.Sprintf("expires %s (%s)", exp.Format("2006-01-02"), Plural(days, "day"))))
		}
	}
	switch {
	case s.Timer:
		rows = append(rows, row("timer", ui.Done, "on"))
	case s.LoginPending:
		rows = append(rows, row("timer", ui.Skipped, "off until the Claude login"))
	default:
		rows = append(rows, row("timer", ui.Skipped, "off"))
	}
	switch {
	case s.SessionsErr != "":
		rows = append(rows, row("sessions", ui.Skipped, "unknown: "+s.SessionsErr))
	case len(s.Sessions) == 0:
		rows = append(rows, row("sessions", ui.Done, "none"))
	default:
		rows = append(rows, row("sessions", ui.Done, strings.Join(s.Sessions, ", ")))
	}
	return rows
}

// Todos is one line per FAIL line, with its next step after a semicolon.
func (s Summary) Todos() []string {
	var out []string
	for _, f := range s.Failed {
		out = append(out, strings.TrimSpace(strings.TrimPrefix(f, "FAIL")))
	}
	return out
}
