package steps

import (
	"context"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

const (
	daemonDefault  = `{"log-driver":"json-file"}`
	daemonNftables = `{"firewall-backend":"nftables"}`
)

const (
	ruleForward    = "-i incusbr0 -j DOCKER-FORWARD"
	ruleAcceptIn   = "-i incusbr0 -j ACCEPT"
	ruleAcceptBack = "-o incusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT"
)

var wantRules = []string{ruleForward, ruleAcceptIn, ruleAcceptBack}

func isDocker(unit string) bool {
	return slices.Contains([]string{"docker", "docker.service", "docker.socket", "dockerd"}, unit)
}

// touchesDocker reports whether c could start, stop, reload or kill dockerd. It reads c as words,
// so a verb or unit in any order among flags and other units counts, as does one behind sudo,
// sh -c or a ; or &&.
func touchesDocker(c string) bool {
	c = strings.NewReplacer("'", " ", `"`, " ", "`", " ", "(", " ", ")", " ", "&", ";", "|", ";", "\n", ";").Replace(c)
	for _, seg := range strings.Split(c, ";") {
		f := strings.Fields(seg)
		for i, w := range f {
			var words []string // the arguments after w, without flags
			for _, a := range f[i+1:] {
				if !strings.HasPrefix(a, "-") {
					words = append(words, a)
				}
			}
			switch base := path.Base(w); {
			case base == "systemctl":
				readOnly := []string{"is-active", "is-enabled", "is-failed", "status", "show", "cat", "list-units", "list-unit-files"}
				if len(words) > 0 && !slices.Contains(readOnly, words[0]) && slices.ContainsFunc(words[1:], isDocker) {
					return true
				}
			case base == "service" || base == "invoke-rc.d":
				if len(words) > 0 && isDocker(words[0]) && (len(words) < 2 || words[1] != "status") {
					return true
				}
			case strings.HasSuffix(w, "init.d/docker"):
				if len(words) == 0 || words[0] != "status" {
					return true
				}
			case base == "kill" || base == "pkill" || base == "killall":
				if slices.ContainsFunc(words, func(a string) bool { return isDocker(a) || strings.Contains(a, "dockerd") }) {
					return true
				}
			case base == "dockerd":
				if i == 0 || slices.Contains([]string{"sudo", "-n", "env", "exec", "nohup", "--", "-c"}, f[i-1]) {
					return true
				}
			}
		}
	}
	return false
}

// dockerUntouched fails when any command could stop, start or reload dockerd.
func dockerUntouched(t *testing.T, calls []string) {
	t.Helper()
	for _, c := range calls {
		if touchesDocker(c) {
			t.Errorf("touches Docker: %s", c)
		}
	}
}

// listRules is iptables -S DOCKER-USER for a chain holding rules, top first.
func listRules(rules ...string) string {
	s := "-N DOCKER-USER\n"
	for _, r := range rules {
		s += "-A DOCKER-USER " + r + "\n"
	}
	return s
}

// dockerMachine has Docker 28 or newer running with the given daemon.json and, when installed,
// the unit and all three iptables rules in order. ip6tables has no DOCKER-USER chain unless a
// test adds one.
func dockerMachine(t *testing.T, daemon string, installed bool) (*host.Fake, *Env) {
	t.Helper()
	f := host.NewFake().
		On("systemctl is-active --quiet docker.service", 0, "").
		On("iptables -w -L DOCKER-FORWARD -n", 0, "")
	e := testEnv(t, f)
	writeFile(t, e, daemonJSON, daemon)
	if installed {
		writeFile(t, e, dockerUnitPath, dockerUnit())
		f.On("systemctl is-enabled --quiet "+dockerUnitName, 0, "").
			On("iptables -w -S DOCKER-USER", 0, listRules(wantRules...))
		for _, r := range wantRules {
			f.On("iptables -w -C DOCKER-USER "+r, 0, "")
		}
	}
	return f, e
}

func TestDockerChecksEveryRuleAndTheirOrder(t *testing.T) {
	for _, r := range wantRules {
		f, e := dockerMachine(t, daemonDefault, true)
		f.On("iptables -w -C DOCKER-USER "+r, 1, "")
		if ok, detail, _ := (Docker{}).Check(context.Background(), e); ok || detail != "iptables DOCKER-USER lacks "+r {
			t.Errorf("%v %q", ok, detail)
		}
		dockerUntouched(t, f.Calls)
	}
	for _, order := range [][]string{
		{ruleAcceptIn, ruleForward, ruleAcceptBack},
		{ruleAcceptIn, ruleAcceptBack, ruleForward},
	} {
		f, e := dockerMachine(t, daemonDefault, true)
		f.On("iptables -w -S DOCKER-USER", 0, listRules(order...))
		if ok, detail, _ := (Docker{}).Check(context.Background(), e); ok || !strings.HasPrefix(detail, "iptables DOCKER-USER has "+ruleAcceptIn+" above "+ruleForward) {
			t.Errorf("%q: %v %q", order, ok, detail)
		}
		dockerUntouched(t, f.Calls)
	}
	// The conntrack ACCEPT matches only traffic to the VM, so where it sits does not matter.
	f, e := dockerMachine(t, daemonDefault, true)
	f.On("iptables -w -S DOCKER-USER", 0, listRules(ruleAcceptBack, ruleForward, ruleAcceptIn))
	if ok, detail, _ := (Docker{}).Check(context.Background(), e); !ok {
		t.Errorf("%q", detail)
	}
}

// Before 28, Docker has no DOCKER-FORWARD chain, and the unit's jump to it cannot be inserted.
func TestDockerNeedsDockerForward(t *testing.T) {
	for _, tc := range []struct {
		on   func(*host.Fake)
		want string
	}{
		{func(f *host.Fake) { f.On("iptables -w -L DOCKER-FORWARD -n", 1, "") },
			"Docker 28 or newer is needed: DOCKER-FORWARD chain missing"},
		{func(f *host.Fake) { f.On("ip6tables -w -L DOCKER-USER -n", 0, "") },
			"Docker 28 or newer is needed: ip6tables DOCKER-FORWARD chain missing"},
	} {
		f, e := dockerMachine(t, daemonDefault, false)
		tc.on(f)
		if ok, detail, _ := (Docker{}).Check(context.Background(), e); ok || detail != tc.want {
			t.Errorf("%v %q", ok, detail)
		}
		if err := (Docker{}).Apply(context.Background(), e); err == nil || err.Error() != tc.want {
			t.Errorf("%v", err)
		}
		if _, err := os.Stat(e.Path(dockerUnitPath)); err == nil {
			t.Error("wrote the unit for a Docker without DOCKER-FORWARD")
		}
		dockerUntouched(t, f.Calls)
	}
}

func TestDockerSkippedWhenNotRunning(t *testing.T) {
	f := host.NewFake().On("systemctl is-active --quiet docker.service", 3, "inactive\n")
	if ok, detail, err := (Docker{}).Check(context.Background(), testEnv(t, f)); !ok || err != nil || detail != "Docker not running" {
		t.Fatalf("%v %q %v", ok, detail, err)
	}
	dockerUntouched(t, f.Calls)
}

func TestDockerInstalledPassesWithoutWriting(t *testing.T) {
	f, e := dockerMachine(t, daemonDefault, true)
	ok, detail, err := Docker{}.Check(context.Background(), e)
	if !ok || err != nil || !strings.Contains(detail, "iptables backend") {
		t.Fatalf("%v %q %v", ok, detail, err)
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "-I ") || strings.Contains(c, "systemctl enable") {
			t.Errorf("writes: %s", c)
		}
	}
	dockerUntouched(t, f.Calls)
}

func TestDockerChecksIPv6RulesWhereDockerMadeTheChain(t *testing.T) {
	f, e := dockerMachine(t, daemonDefault, true)
	f.On("ip6tables -w -L DOCKER-USER -n", 0, "").
		On("ip6tables -w -L DOCKER-FORWARD -n", 0, "")
	ok, detail, _ := Docker{}.Check(context.Background(), e)
	if ok || detail != "ip6tables DOCKER-USER lacks "+ruleForward {
		t.Fatalf("%v %q", ok, detail)
	}
	for _, r := range wantRules {
		f.On("ip6tables -w -C DOCKER-USER "+r, 0, "")
	}
	f.On("ip6tables -w -S DOCKER-USER", 0, listRules(ruleAcceptIn, ruleForward, ruleAcceptBack))
	if ok, detail, _ := (Docker{}).Check(context.Background(), e); ok || !strings.HasPrefix(detail, "ip6tables DOCKER-USER has ") {
		t.Fatalf("%v %q", ok, detail)
	}
	f.On("ip6tables -w -S DOCKER-USER", 0, listRules(wantRules...))
	if ok, detail, _ := (Docker{}).Check(context.Background(), e); !ok {
		t.Fatalf("%q", detail)
	}
	dockerUntouched(t, f.Calls)
}

func TestDockerStoppedButEnabledStillGetsTheUnit(t *testing.T) {
	f := host.NewFake().
		On("systemctl is-active --quiet docker.service", 3, "").
		On("systemctl is-enabled --quiet docker.service", 0, "").
		On("systemctl daemon-reload", 0, "").
		On("systemctl enable "+dockerUnitName, 0, "")
	e := testEnv(t, f)
	if ok, detail, _ := (Docker{}).Check(context.Background(), e); ok || !strings.Contains(detail, dockerUnitPath) {
		t.Fatalf("%v %q", ok, detail)
	}
	if err := (Docker{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	// Restarting the oneshot with Docker stopped would leave it active, and the start that follows
	// would not run it again.
	if f.Ran("systemctl restart " + dockerUnitName) {
		t.Fatalf("%v", f.Calls)
	}
	dockerUntouched(t, f.Calls)
}

func TestDockerStoppedWithTheUnitEnabledPasses(t *testing.T) {
	f := host.NewFake().
		On("systemctl is-active --quiet docker.service", 3, "").
		On("systemctl is-active --quiet docker.socket", 0, "").
		On("systemctl is-enabled --quiet "+dockerUnitName, 0, "")
	e := testEnv(t, f)
	writeFile(t, e, dockerUnitPath, dockerUnit())
	ok, detail, _ := Docker{}.Check(context.Background(), e)
	if !ok || !strings.Contains(detail, "next start") {
		t.Fatalf("%v %q", ok, detail)
	}
	dockerUntouched(t, f.Calls)
}

func TestDockerNftablesBackendStopsCreate(t *testing.T) {
	f, e := dockerMachine(t, daemonNftables, false)
	ok, detail, _ := Docker{}.Check(context.Background(), e)
	if ok || !strings.Contains(detail, "nftables") {
		t.Fatalf("%v %q", ok, detail)
	}
	err := Docker{}.Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "nftables") || !strings.Contains(err.Error(), "DOCKER-USER") {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.Path(dockerUnitPath)); err == nil {
		t.Fatal("wrote the unit for an nftables Docker")
	}
	dockerUntouched(t, f.Calls)
}

func TestDockerApplyInstallsTheUnitAndNeverRestartsDocker(t *testing.T) {
	f, e := dockerMachine(t, daemonDefault, false)
	f.On("systemctl daemon-reload", 0, "").
		On("systemctl enable "+dockerUnitName, 0, "").
		On("systemctl restart "+dockerUnitName, 0, "")
	if err := (Docker{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(e.Path(dockerUnitPath))
	if err != nil || string(got) != dockerUnit() {
		t.Fatalf("unit %q (%v)", got, err)
	}
	if fi, _ := os.Stat(e.Path(dockerUnitPath)); fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v", fi.Mode())
	}
	want := []string{
		"systemctl daemon-reload",
		"systemctl enable " + dockerUnitName,
		"systemctl restart " + dockerUnitName,
	}
	if got := f.Calls[len(f.Calls)-len(want):]; !slices.Equal(got, want) {
		t.Fatalf("%q", f.Calls)
	}
	dockerUntouched(t, f.Calls)
}

func TestDockerUnitShape(t *testing.T) {
	u := dockerUnit()
	for _, want := range []string{"After=docker.service\n", "PartOf=docker.service\n", "WantedBy=docker.service\n", "Type=oneshot\n", "RemainAfterExit=yes\n"} {
		if !strings.Contains(u, want) {
			t.Errorf("lacks %q", want)
		}
	}
	// A lone $t or $m would be expanded by systemd before the shell sees it.
	if regexp.MustCompile(`[^$]\$[tm]\b`).MatchString(u) {
		t.Fatalf("unescaped variable:\n%s", u)
	}
	dockerUntouched(t, strings.Split(u, "\n"))
}

// execLine is the shell script of the unit's key= line, unescaped the way systemd does.
func execLine(t *testing.T, key string) string {
	t.Helper()
	for _, l := range strings.Split(dockerUnit(), "\n") {
		if v, ok := strings.CutPrefix(l, key+"="); ok {
			v = strings.TrimSuffix(strings.TrimPrefix(v, "/bin/sh -c '"), "'")
			return strings.ReplaceAll(v, "$$", "$")
		}
	}
	t.Fatalf("no %s line", key)
	return ""
}

// runUnitScript runs the unit's ExecStart script against fake iptables and ip6tables that log their
// arguments after -w and exit as exits gives for the longest key that prefixes "<tool> <args>",
// else 0. A call without -w fails the test. ip6tables has no DOCKER-USER chain unless exits says
// otherwise. It returns the log and the script's exit status.
func runUnitScript(t *testing.T, exits map[string]int) ([]string, int) {
	t.Helper()
	bin := t.TempDir()
	logf := filepath.Join(bin, "log")
	codes := map[string]int{"ip6tables -L": 1}
	maps.Copy(codes, exits)
	keys := slices.SortedFunc(maps.Keys(codes), func(a, b string) int { return len(b) - len(a) })
	fake := "#!/bin/sh\n" +
		"[ \"$1\" = -w ] || { echo \"no -w: $(basename \"$0\") $*\" >> " + logf + "; exit 99; }\nshift\n" +
		"echo \"$(basename \"$0\") $*\" >> " + logf + "\n" +
		"case \"$(basename \"$0\") $*\" in\n"
	for _, k := range keys {
		fake += "  \"" + k + "\"*) exit " + strconv.Itoa(codes[k]) + " ;;\n"
	}
	fake += "esac\nexit 0\n"
	for _, n := range []string{"iptables", "ip6tables"} {
		if err := os.WriteFile(filepath.Join(bin, n), []byte(fake), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	c := exec.Command("sh", "-c", execLine(t, "ExecStart"))
	c.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin")
	out, err := c.CombinedOutput()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatalf("%v %s", err, out)
	}
	b, _ := os.ReadFile(logf)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	dockerUntouched(t, lines)
	for _, l := range lines {
		// Without -w, iptables-legacy fails at once while Docker or firewalld holds the xtables lock.
		if strings.HasPrefix(l, "no -w: ") {
			t.Errorf("%s", l)
		}
	}
	return lines, c.ProcessState.ExitCode()
}

// replay applies the -I and -D calls in log to tool's chain, top first.
func replay(tool string, chain []string, log []string) []string {
	chain = slices.Clone(chain)
	for _, l := range log {
		if r, ok := strings.CutPrefix(l, tool+" -I DOCKER-USER "); ok {
			chain = append([]string{r}, chain...)
		} else if r, ok := strings.CutPrefix(l, tool+" -D DOCKER-USER "); ok {
			if i := slices.Index(chain, r); i >= 0 {
				chain = slices.Delete(chain, i, i+1)
			}
		}
	}
	return chain
}

func TestDockerUnitStartAddsMissingRules(t *testing.T) {
	got, code := runUnitScript(t, map[string]int{"iptables -C": 1})
	if chain := replay("iptables", nil, got); !slices.Equal(chain, wantRules) {
		t.Fatalf("%q from %q", chain, got)
	}
	if got[len(got)-1] != "ip6tables -L DOCKER-USER -n" || slices.ContainsFunc(got, func(l string) bool { return strings.HasPrefix(l, "ip6tables -I") }) {
		t.Fatalf("%q", got)
	}
	// No IPv6 DOCKER-USER chain is Docker running without IPv6, not a failure.
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// Whatever is missing, a rule inserted at the top must not end above a rule meant to sit over it.
func TestDockerUnitStartKeepsTheJumpAboveTheAccept(t *testing.T) {
	for _, missing := range [][]string{
		{ruleForward}, // a chain from the two-rule unit
		{ruleAcceptIn},
		{ruleAcceptBack},
	} {
		exits := map[string]int{}
		var present []string
		for _, r := range wantRules {
			if slices.Contains(missing, r) {
				exits["iptables -C DOCKER-USER "+r] = 1
			} else {
				present = append(present, r)
			}
		}
		got, code := runUnitScript(t, exits)
		if chain := replay("iptables", present, got); !slices.Equal(chain, wantRules) || code != 0 {
			t.Errorf("missing %q: %q (exit %d) from %q", missing, chain, code, got)
		}
	}
}

func TestDockerUnitStartOrdersIPv6TooWhereDockerMadeTheChain(t *testing.T) {
	got, code := runUnitScript(t, map[string]int{"ip6tables -L": 0, "ip6tables -C DOCKER-USER " + ruleForward: 1})
	if chain := replay("ip6tables", []string{ruleAcceptIn, ruleAcceptBack}, got); !slices.Equal(chain, wantRules) || code != 0 {
		t.Fatalf("%q (exit %d) from %q", chain, code, got)
	}
}

func TestDockerUnitStartAddsNothingTwice(t *testing.T) {
	lines, code := runUnitScript(t, nil)
	for _, line := range lines {
		if strings.Contains(line, " -I ") {
			t.Fatalf("inserted a present rule: %s", line)
		}
	}
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// The unit's status is the only trace a boot leaves, so a VM left behind FORWARD's DROP must fail it.
func TestDockerUnitStartFailsWhenAnIPv4RuleIsNotInserted(t *testing.T) {
	for name, exits := range map[string]map[string]int{
		"insert fails": {"iptables -C": 1, "iptables -I": 1},
		"no chain":     {"iptables -L": 1, "iptables -C": 1, "iptables -I": 1},
		// The jump's -C and -I fail where Docker made no DOCKER-FORWARD chain to jump to.
		"no DOCKER-FORWARD": {"iptables -C DOCKER-USER " + ruleForward: 2, "iptables -I DOCKER-USER " + ruleForward: 2},
	} {
		if _, code := runUnitScript(t, exits); code == 0 {
			t.Errorf("%s: exit 0", name)
		}
	}
}

// A stopped Docker leaves FORWARD at DROP, so the rules must outlive it.
func TestDockerUnitKeepsTheRulesWhenDockerStops(t *testing.T) {
	if u := dockerUnit(); strings.Contains(u, "ExecStop") {
		t.Fatalf("removes the rules:\n%s", u)
	}
	// The start script deletes only a rule it then inserts again.
	got, _ := runUnitScript(t, map[string]int{"iptables -C": 1})
	for i, l := range got {
		if r, ok := strings.CutPrefix(l, "iptables -D "); ok && (i+1 == len(got) || got[i+1] != "iptables -I "+r) {
			t.Fatalf("%q", got)
		}
	}
}

func TestDockerWithoutDaemonJSONUsesIptables(t *testing.T) {
	f, e := dockerMachine(t, daemonDefault, true)
	if err := os.Remove(e.Path(daemonJSON)); err != nil {
		t.Fatal(err)
	}
	if ok, detail, _ := (Docker{}).Check(context.Background(), e); !ok || !strings.Contains(detail, "iptables backend") {
		t.Fatalf("%v %q", ok, detail)
	}
	dockerUntouched(t, f.Calls)
}

func TestDockerBackend(t *testing.T) {
	for in, want := range map[string]string{
		daemonNftables:                    "nftables",
		`{"firewall-backend":"iptables"}`: "iptables",
		daemonDefault:                     "iptables",
		"not json":                        "iptables",
		"":                                "iptables",
	} {
		if got := dockerBackend(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}
