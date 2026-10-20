package create

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

const (
	daemonDefault  = `{"log-driver":"json-file"}`
	daemonNftables = `{"firewall-backend":"nftables"}`
)

// dockerMachine has Docker running with the given daemon.json and, when installed, the unit and
// both iptables rules. ip6tables has no DOCKER-USER chain unless a test adds one.
func dockerMachine(daemon string, installed bool) *host.Fake {
	f := host.NewFake().
		On("systemctl is-active --quiet docker.service", 0, "").
		On("sudo -n cat "+daemonJSON, 0, daemon)
	if installed {
		f.On("sudo -n cat "+dockerUnitPath, 0, dockerUnit()).
			On("systemctl is-enabled --quiet "+dockerUnitName, 0, "").
			On("sudo -n iptables -C DOCKER-USER -i incusbr0 -j ACCEPT", 0, "").
			On("sudo -n iptables -C DOCKER-USER -o incusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT", 0, "")
	}
	return f
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
	f := dockerMachine(daemonDefault, true).On("sudo -n ip6tables -L DOCKER-USER -n", 0, "")
	e, _ := testEnv(f)
	ok, detail, _ := Docker{}.Check(context.Background(), e)
	if ok || !strings.HasPrefix(detail, "ip6tables DOCKER-USER lacks -i incusbr0 -j ACCEPT") {
		t.Fatalf("%v %q", ok, detail)
	}
	f.On("sudo -n ip6tables -C DOCKER-USER -i incusbr0 -j ACCEPT", 0, "").
		On("sudo -n ip6tables -C DOCKER-USER -o incusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT", 0, "")
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
	// A lone $t would be expanded by systemd before the shell sees it.
	if regexp.MustCompile(`[^$]\$t\b`).MatchString(u) {
		t.Fatalf("unescaped $t:\n%s", u)
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

// runUnitScript runs the unit's key= script against fake iptables and ip6tables that log their
// arguments. iptables has DOCKER-USER, and -C exits checkExit; ip6tables has no DOCKER-USER chain.
func runUnitScript(t *testing.T, key, checkExit string) []string {
	t.Helper()
	bin := t.TempDir()
	logf := filepath.Join(bin, "log")
	fake := "#!/bin/sh\necho \"$(basename \"$0\") $*\" >> " + logf + "\n" +
		"case \"$(basename \"$0\") $1\" in\n" +
		"  \"iptables -L\") exit 0 ;;\n" +
		"  \"iptables -C\") exit " + checkExit + " ;;\n" +
		"  \"ip6tables -L\") exit 1 ;;\n" +
		"esac\nexit 0\n"
	for _, n := range []string{"iptables", "ip6tables"} {
		if err := os.WriteFile(filepath.Join(bin, n), []byte(fake), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	c := exec.Command("sh", "-c", execLine(t, key))
	c.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v %s", key, err, out)
	}
	b, _ := os.ReadFile(logf)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	dockerUntouched(t, lines)
	return lines
}

func TestDockerUnitStartAddsMissingRules(t *testing.T) {
	want := []string{
		"iptables -L DOCKER-USER -n",
		"iptables -C DOCKER-USER -o incusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT",
		"iptables -I DOCKER-USER -o incusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT",
		"iptables -C DOCKER-USER -i incusbr0 -j ACCEPT",
		"iptables -I DOCKER-USER -i incusbr0 -j ACCEPT",
		"ip6tables -L DOCKER-USER -n",
	}
	if got := runUnitScript(t, "ExecStart", "1"); !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
}

func TestDockerUnitStartAddsNothingTwice(t *testing.T) {
	for _, line := range runUnitScript(t, "ExecStart", "0") {
		if strings.Contains(line, " -I ") {
			t.Fatalf("inserted a present rule: %s", line)
		}
	}
}

// A stopped Docker leaves FORWARD at DROP, so the rules must outlive it.
func TestDockerUnitKeepsTheRulesWhenDockerStops(t *testing.T) {
	u := dockerUnit()
	if strings.Contains(u, "ExecStop") || strings.Contains(u, " -D ") {
		t.Fatalf("removes the rules:\n%s", u)
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
