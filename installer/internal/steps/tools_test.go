package steps

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

func toolsEnv(t *testing.T, f *host.Fake, withMise bool) *Env {
	e := testEnv(t, f)
	e.Answers.Tools = map[string]string{"go": "1.27.1", "just": "1.58.0"}
	if withMise {
		p := e.HomePath(".local/bin/mise")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

const miseBin = "/home/flocc/.local/bin/mise"

func TestToolsIsDoneWithNoneInTheHostFile(t *testing.T) {
	e := testEnv(t, host.NewFake())
	if ok, detail, _ := (Tools{}).Check(context.Background(), e); !ok || detail != "none in the host file" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestToolsNamesTheOnesWithoutAGlobalVersion(t *testing.T) {
	f := host.NewFake().On(miseBin+" current go", 0, "").On(miseBin+" current just", 0, "1.58.0\n")
	ok, detail, err := (Tools{}).Check(context.Background(), toolsEnv(t, f, true))
	if ok || err != nil || detail != "not set: go 1.27.1" {
		t.Fatalf("%v %q %v", ok, detail, err)
	}
}

func TestToolsTakesAVersionPrefixAsSetButNotAPartialNumber(t *testing.T) {
	e := toolsEnv(t, host.NewFake().On(miseBin+" current go", 0, "1.27.1\n").On(miseBin+" current just", 0, "1.58.0\n"), true)
	e.Answers.Tools = map[string]string{"go": "1.27", "just": "1.5"}
	if ok, detail, _ := (Tools{}).Check(context.Background(), e); ok || detail != "not set: just 1.5" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestToolsApplySetsOnlyTheMissingOnes(t *testing.T) {
	f := host.NewFake().On(miseBin+" current go", 0, "").On(miseBin+" current just", 0, "1.58.0").
		On(miseBin+" use -g go@1.27.1", 0, "")
	if err := (Tools{}).Apply(context.Background(), toolsEnv(t, f, true)); err != nil {
		t.Fatal(err)
	}
	if !f.Ran(miseBin+" use -g go@1.27.1") || f.Ran(miseBin+" use -g just@1.58.0") {
		t.Fatalf("calls %q", f.Calls)
	}
}

func TestToolsWithoutMiseSaysToInstallIt(t *testing.T) {
	e := toolsEnv(t, host.NewFake(), false)
	if ok, detail, _ := (Tools{}).Check(context.Background(), e); ok || detail != "mise is not installed for the service user" {
		t.Fatalf("%v %q", ok, detail)
	}
	if err := (Tools{}).Apply(context.Background(), e); err == nil {
		t.Fatal("applied without mise")
	}
}
