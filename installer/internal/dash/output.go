package dash

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/ship"
)

const (
	outputEvery = 2 * time.Second
	shipToSee   = "ship to see session output"
	pagerLines  = 2000
	loading     = "loading"
)

// outputMsg is one fetch of a screen's output pane. gen names the chain of fetches it belongs to:
// leaving a screen bumps Model.outGen, so a late answer of the screen left is dropped.
type outputMsg struct {
	gen   int
	title string   // "session" or "log"
	lines []string // sanitised
	note  string   // shipToSee, an error, or ""
	empty string   // shown when lines and note are both empty
}

type outputTick struct{ gen int }

// oldHost holds when the host's flocc predates the command: argparse exits 2 naming it.
func oldHost(res host.Result) bool {
	return res.Code == 2 && (strings.Contains(res.Stderr, "invalid choice") || strings.Contains(res.Stderr, "unrecognized arguments"))
}

// attempt is one host command that may fill an output pane.
type attempt struct {
	title string
	args  []string
	empty string // what the pane says when the command prints nothing
}

// fetchOutput runs the attempts in order, going on to the next only after an exit of 3, which
// peek uses for "no live session".
func fetchOutput(ctx context.Context, src Source, h ship.FleetHost, gen int, tries []attempt) outputMsg {
	var (
		res host.Result
		err error
	)
	msg := outputMsg{gen: gen}
	for _, a := range tries {
		msg.title, msg.empty = a.title, a.empty
		if res, err = src.Run(ctx, h, a.args); err != nil || res.Code != 3 {
			break
		}
	}
	switch {
	case err != nil:
		msg.note = firstLine(err.Error())
	case oldHost(res):
		msg.note = shipToSee
	case res.Code != 0:
		msg.note = firstLine(res.Stderr)
		if msg.note == "" {
			msg.note = firstLine(res.Stdout)
		}
		if msg.note == "" {
			msg.note = fmt.Sprintf("exit %d", res.Code)
		}
	case strings.TrimSpace(res.Stdout) != "":
		for _, l := range strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n") {
			msg.lines = append(msg.lines, saved.Sanitize(l))
		}
	}
	return msg
}

func firstLine(s string) string { return saved.Sanitize(ship.FirstLine(s)) }

func (m *Model) hostByName(name string) (ship.FleetHost, bool) {
	for _, h := range m.hosts {
		if h.host.Name == name {
			return h.host, true
		}
	}
	return ship.FleetHost{}, false
}

// issueOutput is peek, falling back on exit 3 to logs --issue.
func (m *Model) issueOutput(gen int, h ship.FleetHost, key string, n int) tea.Cmd {
	return m.fetchCmd(gen, h, []attempt{
		{"session", []string{"peek", key, "-n", strconv.Itoa(n)}, ""},
		{"log", []string{"logs", "--issue", key, "-n", strconv.Itoa(n)}, "no lines name " + key + " yet"},
	})
}

func (m *Model) hostOutput(gen int, h ship.FleetHost, n int) tea.Cmd {
	return m.fetchCmd(gen, h, []attempt{{"log", []string{"logs", "-n", strconv.Itoa(n)}, ""}})
}

func (m *Model) fetchCmd(gen int, h ship.FleetHost, tries []attempt) tea.Cmd {
	src, ctx := m.opts.Source, m.ctx
	return func() tea.Msg { return fetchOutput(ctx, src, h, gen, tries) }
}

// outputFor starts a fetch of n lines for the screen that owns the output, on the current chain.
func (m *Model) outputFor(s screen, n int) tea.Cmd {
	h, ok := m.hostByName(m.cur[0])
	if !ok {
		return nil
	}
	if s == hostScreen {
		return m.hostOutput(m.outGen, h, n)
	}
	return m.issueOutput(m.outGen, h, m.cur[1], n)
}

// enter opens a detail screen on m.cur and starts its output chain.
func (m *Model) enter(s screen) tea.Cmd {
	m.screen, m.out = s, outputMsg{note: loading}
	m.outGen++
	return m.outputFor(s, m.outRows())
}

// leave returns to s; any answer still out for the screen left is dropped.
func (m *Model) leave(s screen) {
	m.screen = s
	m.outGen++
}

// gotOutput keeps the answer of the current chain and, while the screen wants it, schedules the next.
func (m *Model) gotOutput(msg outputMsg) tea.Cmd {
	m.out = msg
	if m.screen == pagerScreen {
		m.pager.load(msg)
		if !m.pager.follow {
			return nil
		}
	} else if m.screen != issueScreen && m.screen != hostScreen {
		return nil
	}
	gen := m.outGen
	return tea.Tick(outputEvery, func(time.Time) tea.Msg { return outputTick{gen} })
}

// refresh starts the next fetch of the current screen's chain.
func (m *Model) refresh() tea.Cmd {
	switch m.screen {
	case issueScreen, hostScreen:
		return m.outputFor(m.screen, m.outRows())
	case pagerScreen:
		return m.outputFor(m.pager.from, pagerLines)
	}
	return nil
}
