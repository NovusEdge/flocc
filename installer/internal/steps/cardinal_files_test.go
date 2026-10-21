package steps

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCardinalUnitsRenderTheRenewWindow(t *testing.T) {
	for hour, want := range map[int]string{4: "OnCalendar=*-*-* 04,05:00/15", 23: "OnCalendar=*-*-* 23,00:00/15"} {
		timer := string(CardinalUnits(hour)["cardinal-renew.timer"])
		if !strings.Contains(timer, want+"\n") {
			t.Errorf("renew_hour %d: timer lacks %q:\n%s", hour, want, timer)
		}
	}
}

func TestCardinalServiceArmsTheLoopAsOneArgument(t *testing.T) {
	svc := string(CardinalUnits(4)["cardinal.service"])
	var start string
	for _, l := range strings.Split(svc, "\n") {
		if strings.HasPrefix(l, "ExecStart=") {
			start = strings.TrimPrefix(l, "ExecStart=")
		}
	}
	want := `/usr/bin/tmux -L %u new-session -d -s %u -x 200 -y 50 -c %h %h/.local/bin/claude --name %u --remote-control %u --permission-mode auto "/loop 20m Run your check from CLAUDE.md."`
	if start != want {
		t.Fatalf("ExecStart:\n got %s\nwant %s", start, want)
	}
}

// systemd-analyze flags an ExecStart binary that does not exist, so only the unit syntax is held
// against it here: any complaint naming something other than a missing command fails.
func TestCardinalUnitsVerify(t *testing.T) {
	if _, err := exec.LookPath("systemd-analyze"); err != nil {
		t.Skip("systemd-analyze is not installed")
	}
	dir := t.TempDir()
	var paths []string
	for name, b := range CardinalUnits(4) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	out, _ := exec.Command("systemd-analyze", append([]string{"--user", "verify"}, paths...)...).CombinedOutput()
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l == "" || strings.Contains(l, "is not executable") || strings.Contains(l, "No such file") {
			continue
		}
		t.Errorf("systemd-analyze: %s", l)
	}
}

func TestCardinalBriefFillsEveryPlaceholder(t *testing.T) {
	b := string(CardinalBrief("cardinal-test", "zer0-day-nimble"))
	if strings.Contains(b, "{{") || !strings.Contains(b, "`cardinal-test` user on zer0-day-nimble") {
		t.Fatalf("brief:\n%s", b)
	}
}
