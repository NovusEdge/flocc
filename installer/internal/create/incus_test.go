package create

import (
	"context"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

const meminfo16 = "MemTotal:       32000000 kB\nMemFree:         1000000 kB\nMemAvailable:   16777216 kB\n"

func readyIncus() *host.Fake {
	return host.NewFake().
		On("incus query /1.0/networks/incusbr0", 0, `{"managed":true,"type":"bridge"}`).
		On("incus profile device get default root pool", 0, "default\n").
		On("incus query /1.0/storage-pools/default/resources", 0, `{"space":{"used":10737418240,"total":107374182400}}`).
		On("cat /proc/meminfo", 0, meminfo16)
}

func TestIncusReady(t *testing.T) {
	e, _ := testEnv(readyIncus())
	ok, detail, err := Incus{}.Check(context.Background(), e)
	if err != nil || !ok || !strings.Contains(detail, "pool default 90.0 GiB free") {
		t.Fatalf("%v %q %v", ok, detail, err)
	}
}

func TestIncusWithoutTheBridgeStopsWithTheCommands(t *testing.T) {
	f := readyIncus().On("incus query /1.0/networks/incusbr0", 1, "")
	e, _ := testEnv(f)
	if ok, _, _ := (Incus{}).Check(context.Background(), e); ok {
		t.Fatal("ready without a bridge")
	}
	err := Incus{}.Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "incus admin init --minimal") || !strings.Contains(err.Error(), "incus network create incusbr0") {
		t.Fatal(err)
	}
}

func TestIncusPrintsSudoCommandsWhenIncusNeedsSudo(t *testing.T) {
	f := readyIncus().
		On("sudo -n incus query /1.0/networks/incusbr0", 1, "").
		On("sudo -n incus profile device get default root pool", 1, "")
	e, _ := testEnv(f)
	e.incusSudo = true
	err := Incus{}.Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "sudo incus network create incusbr0") || !strings.Contains(err.Error(), "sudo incus admin init --minimal") {
		t.Fatal(err)
	}
	e2, _ := testEnv(readyIncus().
		On("sudo -n incus query /1.0/networks/incusbr0", 0, `{"managed":true,"type":"bridge"}`).
		On("sudo -n incus profile device get default root pool", 1, ""))
	e2.incusSudo = true
	err = Incus{}.Apply(context.Background(), e2)
	if err == nil || !strings.Contains(err.Error(), "sudo incus profile device add default root disk path=/ pool=default") {
		t.Fatal(err)
	}
}

func TestIncusRefusesAnUnmanagedBridge(t *testing.T) {
	e, _ := testEnv(readyIncus().On("incus query /1.0/networks/incusbr0", 0, `{"managed":false,"type":"bridge"}`))
	if ok, _, _ := (Incus{}).Check(context.Background(), e); ok {
		t.Fatal("ready with an unmanaged bridge")
	}
}

func TestIncusWithoutARootPool(t *testing.T) {
	e, _ := testEnv(readyIncus().On("incus profile device get default root pool", 1, ""))
	err := Incus{}.Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "incus profile device add default root disk path=/ pool=default") {
		t.Fatal(err)
	}
}

func TestIncusPoolTooSmall(t *testing.T) {
	f := readyIncus().On("incus query /1.0/storage-pools/default/resources", 0, `{"space":{"used":96636764160,"total":107374182400}}`)
	e, _ := testEnv(f)
	ok, detail, _ := Incus{}.Check(context.Background(), e)
	if ok || !strings.Contains(detail, "10.0 GiB free; the VM needs 40 GiB") {
		t.Fatalf("%v %q", ok, detail)
	}
	if err := (Incus{}).Apply(context.Background(), e); err == nil || !strings.Contains(err.Error(), "--disk") {
		t.Fatal(err)
	}
}

func TestIncusTooLittleMemory(t *testing.T) {
	e, _ := testEnv(readyIncus().On("cat /proc/meminfo", 0, "MemAvailable:    4194304 kB\n"))
	ok, detail, _ := Incus{}.Check(context.Background(), e)
	if ok || !strings.Contains(detail, "4.0 GiB of memory available; the VM needs 8 GiB") {
		t.Fatalf("%v %q", ok, detail)
	}
	if err := (Incus{}).Apply(context.Background(), e); err == nil || !strings.Contains(err.Error(), "--memory") {
		t.Fatal(err)
	}
}

func TestIncusSkipsRoomChecksForAnExistingVM(t *testing.T) {
	f := readyIncus().On("incus query /1.0/instances/flocc-a", 0, `{"status":"Running"}`).
		On("cat /proc/meminfo", 0, "MemAvailable:    1024 kB\n")
	e, _ := testEnv(f)
	if ok, _, err := (Incus{}).Check(context.Background(), e); !ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
	if f.Ran("cat /proc/meminfo") || f.Ran("incus query /1.0/storage-pools/default/resources") {
		t.Fatalf("%v", f.Calls)
	}
}

func TestIncusApplyNeverChangesIncus(t *testing.T) {
	f := readyIncus().On("incus query /1.0/networks/incusbr0", 1, "")
	e, _ := testEnv(f)
	Incus{}.Apply(context.Background(), e)
	for _, c := range f.Calls {
		if !strings.HasPrefix(c, "incus query") && !strings.HasPrefix(c, "incus profile device get") && c != "cat /proc/meminfo" {
			t.Errorf("changes the machine: %s", c)
		}
	}
}

func TestMemAvailable(t *testing.T) {
	if b, err := memAvailable(meminfo16); err != nil || b != 16<<30 {
		t.Fatalf("%d %v", b, err)
	}
	if _, err := memAvailable("MemTotal: 1 kB\n"); err == nil {
		t.Fatal("no MemAvailable line accepted")
	}
}
