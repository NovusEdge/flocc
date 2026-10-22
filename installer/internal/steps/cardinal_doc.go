package steps

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
)

// CardinalMarker starts the one line of JSON that ends each cardinal phase's output; the desktop
// takes it out of the stream.
const CardinalMarker = "flocc-cardinal "

// CardinalDoc is what the desktop sends the user pass on stdin. It holds no secret.
type CardinalDoc struct {
	User      string             `json:"user"`
	Machine   string             `json:"machine"` // the machine's host name, never with a user part
	RenewHour int                `json:"renew_hour"`
	Hosts     []CardinalHostFile `json:"hosts"`
	// Tailnet is the desktop's own tailnet addresses, which cardinal must not be able to reach.
	Tailnet []string `json:"tailnet"`
}

type CardinalHostFile struct {
	Name      string `json:"name"`
	Addr      string `json:"addr"`
	KnownHost string `json:"known_host"` // "<addr> ssh-ed25519 AAAA…", read from the host by the desktop
}

// ReportStep is one step's state at the end of a pass.
type ReportStep struct {
	Name    string `json:"name"`
	Detail  string `json:"detail"`
	Done    bool   `json:"done"`
	Pending bool   `json:"pending,omitempty"`
}

// CardinalReport ends the root, user and host passes. Key is cardinal's public key, from the user
// pass; HostKey is the host's ed25519 key line and Installed whether this run installed sshd, from
// the host pass.
type CardinalReport struct {
	Key       string       `json:"key,omitempty"`
	Login     string       `json:"login,omitempty"`
	HostKey   string       `json:"host_key,omitempty"`
	Installed bool         `json:"installed,omitempty"`
	Steps     []ReportStep `json:"steps,omitempty"`
	Notes     []string     `json:"notes,omitempty"`
	Error     string       `json:"error,omitempty"`
}

func (r CardinalReport) JSON() string {
	b, _ := json.Marshal(r)
	return string(b)
}

func (r CardinalReport) Line() string { return CardinalMarker + r.JSON() }

func ParseCardinalLine(line string) (CardinalReport, bool) {
	var r CardinalReport
	rest, ok := strings.CutPrefix(line, CardinalMarker)
	if !ok || json.Unmarshal([]byte(rest), &r) != nil {
		return CardinalReport{}, false
	}
	return r, true
}

// ReportSteps checks every step again for the pass's report; pending names steps whose Apply
// returned ErrPending.
func ReportSteps(ctx context.Context, e *Env, list []Step) []ReportStep {
	var out []ReportStep
	for _, s := range list {
		ok, detail, err := s.Check(ctx, e)
		if err != nil {
			detail = err.Error()
		}
		out = append(out, ReportStep{Name: s.Name(), Detail: detail, Done: ok, Pending: slices.Contains(e.Pending, s.Name())})
	}
	return out
}

// CardinalLoginState is "full", "token-only (<method>)" or "none".
func CardinalLoginState(ctx context.Context, e *Env) string { return claudeLogin(ctx, e) }

// HostDoc is what the desktop sends a host's cardinal-host and cardinal-host-remove phases.
type HostDoc struct {
	Addr    string `json:"addr"`
	Gateway string `json:"gateway"`
	// Key is cardinal's public key, empty before the user pass has made one; OldKeys are keys this
	// install used before, whose lines become Key's.
	Key     string   `json:"key"`
	OldKeys []string `json:"old_keys,omitempty"`
	Install string   `json:"install"` // <user>@<machine host>, the install record's name
}
