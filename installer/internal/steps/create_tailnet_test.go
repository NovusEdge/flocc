package steps

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

const nftList = "table inet flocc-no-tailnet {\n\tchain forward {\n\t\ttype filter hook forward priority filter - 10; policy accept;\n\t\tiifname \"incusbr0\" oifname \"tailscale0\" drop\n\t}\n}\n"

func tailnetLoaded(f *host.Fake) *host.Fake {
	return f.On("systemctl is-enabled --quiet "+tailnetUnitName, 0, "").
		On("systemctl is-active --quiet "+tailnetUnitName, 0, "").
		On("nft list table inet flocc-no-tailnet", 0, nftList)
}

func TestTailnetBlockCheckFalseWhenTableMissing(t *testing.T) {
	e := testEnv(t, host.NewFake())
	if ok, detail, err := (TailnetBlock{}).Check(context.Background(), e); ok || err != nil || detail == "" {
		t.Fatalf("%v %q %v", ok, detail, err)
	}
	writeFile(t, e, tailnetFile, tailnetRules)
	writeFile(t, e, tailnetUnitPath, tailnetUnit)
	f := tailnetLoaded(host.NewFake()).On("nft list table inet flocc-no-tailnet", 1, "")
	e.Run = f
	if ok, detail, _ := (TailnetBlock{}).Check(context.Background(), e); ok || !strings.Contains(detail, "drop rule") {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestTailnetBlockApplyWritesFilesAndLoadsThem(t *testing.T) {
	f := tailnetLoaded(host.NewFake().
		On("sh -c command -v nft", 0, "/usr/bin/nft\n").
		On("systemctl daemon-reload", 0, "").
		On("systemctl enable "+tailnetUnitName, 0, "").
		On("systemctl restart "+tailnetUnitName, 0, ""))
	e := testEnv(t, f)
	if err := (TailnetBlock{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{tailnetFile: tailnetRules, tailnetUnitPath: tailnetUnit} {
		got, err := os.ReadFile(e.Path(path))
		if err != nil || string(got) != want {
			t.Fatalf("%s: %q (%v)", path, got, err)
		}
		if fi, _ := os.Stat(e.Path(path)); fi.Mode().Perm() != 0o644 {
			t.Errorf("%s mode %v", path, fi.Mode())
		}
	}
	want := []string{"systemctl daemon-reload", "systemctl enable " + tailnetUnitName, "systemctl restart " + tailnetUnitName}
	if got := f.Calls[len(f.Calls)-len(want):]; !slices.Equal(got, want) {
		t.Fatalf("%q", f.Calls)
	}
	if ok, detail, err := (TailnetBlock{}).Check(context.Background(), e); !ok || err != nil {
		t.Fatalf("%v %q %v", ok, detail, err)
	}
	for _, want := range []string{`iifname "incusbr0" oifname "tailscale0" drop`, "hook forward priority -10", "table inet flocc-no-tailnet"} {
		if !strings.Contains(tailnetRules, want) {
			t.Errorf("rules lack %q", want)
		}
	}
	if strings.Contains(tailnetUnit, "ExecStop") {
		t.Error("stopping the unit reopens the hole")
	}
}

func TestTailnetBlockWarnsWhenTheMachineUsesAnExitNode(t *testing.T) {
	f := tailnetLoaded(host.NewFake()).On("ip route get 1.1.1.1", 0, "1.1.1.1 dev tailscale0 table 52 src 100.64.0.1\n")
	e := testEnv(t, f)
	writeFile(t, e, tailnetFile, tailnetRules)
	writeFile(t, e, tailnetUnitPath, tailnetUnit)
	ok, detail, _ := (TailnetBlock{}).Check(context.Background(), e)
	if !ok || !strings.Contains(detail, "exit node") {
		t.Fatalf("%v %q", ok, detail)
	}
	f.On("ip route get 1.1.1.1", 0, "1.1.1.1 via 192.168.1.1 dev wlan0\n")
	if ok, detail, _ := (TailnetBlock{}).Check(context.Background(), e); !ok || strings.Contains(detail, "exit node") {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestCreateRootStepsPutsTheTailnetBlockFirst(t *testing.T) {
	if _, ok := CreateRootSteps()[0].(TailnetBlock); !ok {
		t.Fatal("an earlier step's Apply error would skip the tailnet block")
	}
}

func TestTailnetBlockApplyFailsWithoutNft(t *testing.T) {
	e := testEnv(t, host.NewFake())
	err := (TailnetBlock{}).Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "nft is not installed") {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.Path(tailnetUnitPath)); err == nil {
		t.Fatal("wrote the unit without nft")
	}
}

func TestCreateRootIdleFalseWithoutTheTailnetBlock(t *testing.T) {
	for _, f := range []*host.Fake{
		host.NewFake(),
		host.NewFake().On("systemctl is-enabled --quiet "+tailnetUnitName, 0, ""),
	} {
		if ok, _ := CreateRootIdle(context.Background(), testEnv(t, f)); ok {
			t.Fatal("idle without the tailnet block")
		}
	}
}
