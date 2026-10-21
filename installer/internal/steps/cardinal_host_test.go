package steps

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

const (
	cardKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIcard cardinal@zer0-day-nimble"
	oldKey  = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIold cardinal@zer0-day-nimble"
)

func hostDoc() HostDoc {
	return HostDoc{Addr: "10.131.47.206", Gateway: "10.131.47.1", Key: cardKey, Install: "cardinal@zer0-day-nimble"}
}

func hostEnv(t *testing.T, f *host.Fake) *Env {
	e := testEnv(t, f)
	e.Distro = host.Distro{ID: "debian", Manager: host.Apt}
	os.MkdirAll(e.Path("/etc/ssh"), 0o755)
	os.WriteFile(e.Path(hostKeyFile), []byte("ssh-ed25519 AAAAhostkey root@flocc-nimble\n"), 0o644)
	return e
}

func sshdFake(installed bool) *host.Fake {
	f := host.NewFake().
		On("sysctl -p "+sysctlFile, 0, "").
		On("apt-get install -y openssh-server", 0, "").
		On("sshd -t", 0, "").
		On("systemctl restart ssh.service", 0, "").
		On("systemctl enable --now ssh.service", 0, "")
	if installed {
		f.On("test -x /usr/sbin/sshd", 0, "")
	}
	return f
}

func TestCardinalHostWritesTheDropInBeforeInstalling(t *testing.T) {
	f := sshdFake(false)
	e := hostEnv(t, f)
	r, err := CardinalHost(context.Background(), e, hostDoc())
	if err != nil {
		t.Fatalf("%v; %v", err, f.Calls)
	}
	// The sysctl write sits after the drop-in's in the code, so its reload before the install
	// shows both files were in place when apt started sshd.
	conf, _ := os.ReadFile(e.Path(sshdDropIn))
	if !strings.Contains(string(conf), "ListenAddress 10.131.47.206\n") || !strings.Contains(string(conf), "PasswordAuthentication no") {
		t.Fatalf("drop-in %q", conf)
	}
	if slices.Index(f.Calls, "sysctl -p "+sysctlFile) > slices.Index(f.Calls, "apt-get install -y openssh-server") {
		t.Fatalf("calls %v", f.Calls)
	}
	if !r.Installed || r.HostKey != "10.131.47.206 ssh-ed25519 AAAAhostkey" {
		t.Fatalf("report %+v", r)
	}
	if _, err := os.Stat(e.Path(sshInstalled)); err != nil {
		t.Fatal("no ssh-installed marker")
	}
}

func TestCardinalHostRerunRestartsNothing(t *testing.T) {
	f := sshdFake(true)
	e := hostEnv(t, f)
	if _, err := CardinalHost(context.Background(), e, hostDoc()); err != nil {
		t.Fatal(err)
	}
	f.Calls = nil
	r, err := CardinalHost(context.Background(), e, hostDoc())
	if err != nil {
		t.Fatal(err)
	}
	if f.Ran("systemctl restart ssh.service") || f.Ran("apt-get install -y openssh-server") || f.Ran("sysctl -p "+sysctlFile) || r.Installed {
		t.Fatalf("rerun calls %v", f.Calls)
	}
	if _, err := os.Stat(e.Path(sshInstalled)); err == nil {
		t.Fatal("marker written for a hand-installed sshd")
	}
}

func TestCardinalHostRefusesANonDebianHost(t *testing.T) {
	e := hostEnv(t, sshdFake(true))
	e.Distro = host.Distro{ID: "arch", Manager: host.Pacman}
	if _, err := CardinalHost(context.Background(), e, hostDoc()); err == nil || !strings.Contains(err.Error(), "Debian") {
		t.Fatalf("err = %v", err)
	}
}

func TestMergeAuthorizedKeysMatchesByKeyNotComment(t *testing.T) {
	existing := "ssh-rsa AAAAowner owner@desktop\n" +
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIold hand-made\n" +
		"ssh-ed25519 AAAAagent agent@x\n" +
		`from="1.2.3.4" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIcard dup` + "\n"
	line := KeyLine("10.131.47.1", cardKey)
	got := string(MergeAuthorizedKeys([]byte(existing), line, []string{cardKey, oldKey}))
	want := "ssh-rsa AAAAowner owner@desktop\n" + line + "\nssh-ed25519 AAAAagent agent@x\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if again := string(MergeAuthorizedKeys([]byte(got), line, []string{cardKey})); again != got {
		t.Fatalf("rerun changed it:\n%s", again)
	}
}

func TestCardinalHostRemoveKeepsAnotherInstallsAccess(t *testing.T) {
	f := sshdFake(false).On("systemctl disable --now ssh.service ssh.socket", 0, "")
	e := hostEnv(t, f)
	ctx := context.Background()
	other := hostDoc()
	other.Install, other.Key = "cardinal-test@zer0-day-nimble", oldKey
	for _, d := range []HostDoc{hostDoc(), other} {
		if _, err := CardinalHost(ctx, e, d); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := CardinalHostRemove(ctx, e, hostDoc()); err != nil {
		t.Fatal(err)
	}
	if f.Ran("systemctl disable --now ssh.service ssh.socket") {
		t.Fatal("ssh disabled while another install uses it")
	}
	keys, _ := os.ReadFile(e.Path(authorizedKeys))
	if strings.Contains(string(keys), "AAAAIcard") || !strings.Contains(string(keys), "AAAAIold") {
		t.Fatalf("authorized_keys:\n%s", keys)
	}
	if _, err := CardinalHostRemove(ctx, e, other); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("systemctl disable --now ssh.service ssh.socket") {
		t.Fatal("last remove left ssh running though setup installed it")
	}
	if _, err := os.Stat(e.Path(sshdDropIn)); !os.IsNotExist(err) {
		t.Fatal("drop-in kept")
	}
}

func TestCardinalHostRemoveKeepsAHandInstalledSSHD(t *testing.T) {
	f := sshdFake(true)
	e := hostEnv(t, f)
	if _, err := CardinalHost(context.Background(), e, hostDoc()); err != nil {
		t.Fatal(err)
	}
	r, err := CardinalHostRemove(context.Background(), e, hostDoc())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.Path(sshdDropIn)); err != nil {
		t.Fatal("drop-in removed from a hand-installed sshd")
	}
	if len(r.Notes) != 1 || !strings.Contains(r.Notes[0], "not installed by setup") {
		t.Fatalf("notes %v", r.Notes)
	}
}

func TestHostDocRefusesInjection(t *testing.T) {
	for _, d := range []HostDoc{
		{Addr: "10.0.0.1\nListenAddress 0.0.0.0", Gateway: "10.0.0.1", Install: "c@m"},
		{Addr: "10.0.0.2", Gateway: `10.0.0.1",command="sh`, Install: "c@m"},
		{Addr: "10.0.0.2", Gateway: "10.0.0.1", Install: "../../etc/x"},
	} {
		if err := validHostDoc(d); err == nil {
			t.Errorf("accepted %+v", d)
		}
	}
}
