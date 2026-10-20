package create

import (
	"context"
	"errors"
	"maps"
	"os"
	"os/exec"
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
func dockerMachine(daemon string, installed bool) *host.Fake {
	f := host.NewFake().
		On("systemctl is-active --quiet docker.service", 0, "").
		On("sudo -n cat "+daemonJSON, 0, daemon).
		On("sudo -n iptables -w -L DOCKER-FORWARD -n", 0, "")
	if installed {
		f.On("sudo -n cat "+dockerUnitPath, 0, dockerUnit()).
			On("systemctl is-enabled --quiet "+dockerUnitName, 0, "").
			On("sudo -n iptables -w -S DOCKER-USER", 0, listRules(wantRules...))
		for _, r := range wantRules {
			f.On("sudo -n iptables -w -C DOCKER-USER "+r, 0, "")
		}
	}
	return f
}

func TestDockerChecksEveryRuleAndTheirOrder(t *testing.T) {
	for _, r := range wantRules {
		f := dockerMachine(daemonDefault, true).On("sudo -n iptables -w -C DOCKER-USER "+r, 1, "")
		e, _ := testEnv(f)
		if ok, detail, _ := (Docker{}).Check(context.Background(), e); ok || detail != "iptables DOCKER-USER lacks "+r {
			t.Errorf("%v %q", ok, detail)
		}
		dockerUntouched(t, f.Calls)
	}
	for _, order := range [][]string{
		{ruleAcceptIn, ruleForward, ruleAcceptBack},
		{ruleAcceptIn, ruleAcceptBack, ruleForward},
	} {
		f := dockerMachine(daemonDefault, true).On("sudo -n iptables -w -S DOCKER-USER", 0, listRules(order...))
		e, _ := testEnv(f)
		if ok, detail, _ := (Docker{}).Check(context.Background(), e); ok || !strings.HasPrefix(detail, "iptables DOCKER-USER has "+ruleAcceptIn+" above "+ruleForward) {
			t.Errorf("%q: %v %q", order, ok, detail)
		}
		dockerUntouched(t, f.Calls)
	}
	// The conntrack ACCEPT matches only traffic to the VM, so where it sits does not matter.
	f := dockerMachine(daemonDefault, true).
		On("sudo -n iptables -w -S DOCKER-USER", 0, listRules(ruleAcceptBack, ruleForward, ruleAcceptIn))
	e, _ := testEnv(f)
	if ok, detail, _ := (Docker{}).Check(context.Background(), e); !ok {
		t.Errorf("%q", detail)
	}
}

// Before 28, Docker has no DOCKER-FORWARD chain, and the unit's jump to it cannot be inserted.
func TestDockerNeedsDockerForward(t *testing.T) {
	for _, tc := range []struct {
		f    *host.Fake
		want string
	}{
		{dockerMachine(daemonDefault, false).On("sudo -n iptables -w -L DOCKER-FORWARD -n", 1, ""),
			"Docker 28 or newer is needed: DOCKER-FORWARD chain missing"},
		{dockerMachine(daemonDefault, false).On("sudo -n ip6tables -w -L DOCKER-USER -n", 0, ""),
			"Docker 28 or newer is needed: ip6tables DOCKER-FORWARD chain missing"},
	} {
		e, _ := testEnv(tc.f)
		if ok, detail, _ := (Docker{}).Check(context.Background(), e); ok || detail != tc.want {
			t.Errorf("%v %q", ok, detail)
		}
		if err := (Docker{}).Apply(context.Background(), e); err == nil || err.Error() != tc.want {
			t.Errorf("%v", err)
		}
		if tc.f.Ran("sudo -n tee " + dockerUnitPath) {
			t.Error("wrote the unit for a Docker without DOCKER-FORWARD")
		}
		dockerUntouched(t, tc.f.Calls)
	}
}

func TestDockerSkippedWhenNotRunning(t *testing.T) {
	f := host.NewFake().On("systemctl is-active --quiet docker.service", 3, "inactive\n")
	e, _ := testEnv(f)
	e.sudo = false
	if ok, detail, err := (Docker{}).Check(context.Background(), e); !ok || err != nil || detail != "Docker not running" {
		t.Fatalf("%v %q %v", ok, detail, err)
	}
	dockerUntouched(t, f.Calls)
}

func TestDockerNeedsPasswordlessSudo(t *testing.T) {
	f := dockerMachine(daemonDefault, false)
	e, _ := testEnv(f)
	e.sudo = false
	if _, _, err := (Docker{}).Check(context.Background(), e); err == nil || !strings.Contains(err.Error(), "passwordless sudo") {
		t.Fatal(err)
	}
	dockerUntouched(t, f.Calls)
}

func TestDockerInstalledPassesWithoutWriting(t *testing.T) {
	f := dockerMachine(daemonDefault, true)
	e, _ := testEnv(f)
	ok, detail, err := Docker{}.Check(context.Background(), e)
	if !ok || err != nil || !strings.Contains(detail, "iptables backend") {
		t.Fatalf("%v %q %v", ok, detail, err)
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "tee") || strings.Contains(c, "-I ") || strings.Contains(c, "systemctl enable") {
			t.Errorf("writes: %s", c)
		}
	}
	dockerUntouched(t, f.Calls)
}

func TestDockerChecksIPv6RulesWhereDockerMadeTheChain(t *testing.T) {
	f := dockerMachine(daemonDefault, true).
		On("sudo -n ip6tables -w -L DOCKER-USER -n", 0, "").
		On("sudo -n ip6tables -w -L DOCKER-FORWARD -n", 0, "")
	e, _ := testEnv(f)
	ok, detail, _ := Docker{}.Check(context.Background(), e)
	if ok || detail != "ip6tables DOCKER-USER lacks "+ruleForward {
		t.Fatalf("%v %q", ok, detail)
	}
	for _, r := range wantRules {
		f.On("sudo -n ip6tables -w -C DOCKER-USER "+r, 0, "")
	}
	f.On("sudo -n ip6tables -w -S DOCKER-USER", 0, listRules(ruleAcceptIn, ruleForward, ruleAcceptBack))
	if ok, detail, _ := (Docker{}).Check(context.Background(), e); ok || !strings.HasPrefix(detail, "ip6tables DOCKER-USER has ") {
		t.Fatalf("%v %q", ok, detail)
	}
	f.On("sudo -n ip6tables -w -S DOCKER-USER", 0, listRules(wantRules...))
	if ok, detail, _ := (Docker{}).Check(context.Background(), e); !ok {
		t.Fatalf("%q", detail)
	}
	dockerUntouched(t, f.Calls)
}

func TestDockerStoppedButEnabledStillGetsTheUnit(t *testing.T) {
	f := host.NewFake().
		On("systemctl is-active --quiet docker.service", 3, "").
		On("systemctl is-enabled --quiet docker.service", 0, "").
		On("sudo -n tee "+dockerUnitPath, 0, "").
		On("sudo -n systemctl daemon-reload", 0, "").
		On("sudo -n systemctl enable "+dockerUnitName, 0, "")
	e, _ := testEnv(f)
	if ok, detail, _ := (Docker{}).Check(context.Background(), e); ok || !strings.Contains(detail, dockerUnitPath) {
		t.Fatalf("%v %q", ok, detail)
	}
	if err := (Docker{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	// Restarting the oneshot with Docker stopped would leave it active, and the start that follows
	// would not run it again.
	if f.Ran("sudo -n systemctl restart " + dockerUnitName) {
		t.Fatalf("%v", f.Calls)
	}
	dockerUntouched(t, f.Calls)
}

func TestDockerStoppedWithTheUnitEnabledPasses(t *testing.T) {
	f := host.NewFake().
		On("systemctl is-active --quiet docker.service", 3, "").
		On("systemctl is-active --quiet docker.socket", 0, "").
		On("sudo -n cat "+dockerUnitPath, 0, dockerUnit()).
		On("systemctl is-enabled --quiet "+dockerUnitName, 0, "")
	e, _ := testEnv(f)
	ok, detail, _ := Docker{}.Check(context.Background(), e)
	if !ok || !strings.Contains(detail, "next start") {
		t.Fatalf("%v %q", ok, detail)
	}
	dockerUntouched(t, f.Calls)
}

func TestDockerNftablesBackendStopsCreate(t *testing.T) {
	f := dockerMachine(daemonNftables, false)
	e, _ := testEnv(f)
	ok, detail, _ := Docker{}.Check(context.Background(), e)
	if ok || !strings.Contains(detail, "nftables") {
		t.Fatalf("%v %q", ok, detail)
	}
	err := Docker{}.Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "nftables") || !strings.Contains(err.Error(), "DOCKER-USER") {
		t.Fatal(err)
	}
	if f.Ran("sudo -n tee " + dockerUnitPath) {
		t.Fatal("wrote the unit for an nftables Docker")
	}
	dockerUntouched(t, f.Calls)
}

func TestDockerApplyInstallsTheUnitAndNeverRestartsDocker(t *testing.T) {
	f := dockerMachine(daemonDefault, false).
		On("sudo -n tee "+dockerUnitPath, 0, "").
		On("sudo -n systemctl daemon-reload", 0, "").
		On("sudo -n systemctl enable "+dockerUnitName, 0, "").
		On("sudo -n systemctl restart "+dockerUnitName, 0, "")
	e, _ := testEnv(f)
	if err := (Docker{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"sudo -n tee " + dockerUnitPath,
		"sudo -n systemctl daemon-reload",
		"sudo -n systemctl enable " + dockerUnitName,
		"sudo -n systemctl restart " + dockerUnitName,
	}
	if got := f.Calls[len(f.Calls)-len(want):]; !slices.Equal(got, want) {
		t.Fatalf("%q", f.Calls)
	}
	dockerUntouched(t, f.Calls)
}

func TestEveryRootCommandIsNonInteractive(t *testing.T) {
	f := dockerMachine(daemonDefault, false)
	e, _ := testEnv(f)
	Docker{}.Check(context.Background(), e)
	Docker{}.Apply(context.Background(), e)
	for _, c := range rootCalls(f.Calls) {
		if !strings.HasPrefix(c, "sudo -n ") {
			t.Errorf("not sudo -n: %s", c)
		}
	}
	dockerUntouched(t, f.Calls)
}

func TestDockerUnitIsWrittenFromStdin(t *testing.T) {
	f := dockerMachine(daemonDefault, false).On("sudo -n tee "+dockerUnitPath, 0, "")
	e, _ := testEnv(f)
	Docker{}.Apply(context.Background(), e)
	if got, ok := f.Stdins["sudo -n tee "+dockerUnitPath]; !ok || got != dockerUnit() {
		t.Fatalf("stdin %q (%v)", got, f.Calls)
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

// daemon.json may be 0600, and tee under the owner's umask 077 leaves the unit 0600.
func TestDockerReadsItsFilesAsRoot(t *testing.T) {
	f := dockerMachine(daemonNftables, false).
		On("cat "+daemonJSON, 1, "").
		On("cat "+dockerUnitPath, 1, "")
	e, _ := testEnv(f)
	if ok, detail, _ := (Docker{}).Check(context.Background(), e); ok || !strings.Contains(detail, "nftables") {
		t.Fatalf("%v %q", ok, detail)
	}
	f = dockerMachine(daemonDefault, true).On("cat "+dockerUnitPath, 1, "")
	e, _ = testEnv(f)
	if ok, detail, _ := (Docker{}).Check(context.Background(), e); !ok {
		t.Fatalf("%q", detail)
	}
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "cat ") {
			t.Errorf("read without sudo: %s", c)
		}
	}
	dockerUntouched(t, f.Calls)
}

func TestDockerWithoutDaemonJSONUsesIptables(t *testing.T) {
	f := dockerMachine(daemonDefault, true).On("sudo -n cat "+daemonJSON, 1, "")
	e, _ := testEnv(f)
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
