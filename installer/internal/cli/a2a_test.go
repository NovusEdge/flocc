package cli

import (
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/keys"
)

type memStore map[keys.Slot]string

func newMemStore() memStore { return memStore{} }

func (m memStore) Get(s keys.Slot) (string, error) { return m[s], nil }
func (m memStore) Set(s keys.Slot, v string) error { m[s] = v; return nil }
func (m memStore) Where() string                   { return "memory" }

func a2aHost(name, ssh string, repos ...string) hosts.Host {
	h := hosts.Host{Name: name, SSH: ssh}
	for _, r := range repos {
		h.Repos = append(h.Repos, answers.Repo{Name: r})
	}
	return h
}

func TestA2AFilesGiveEveryHostEveryToken(t *testing.T) {
	st := newMemStore()
	all := []hosts.Host{
		a2aHost("flocc-nimble", "root@flocc-nimble", "nimblefox"),
		a2aHost("box2", "me@box2", "web"),
		{Name: "sandbox", Daytona: "abc"},
	}
	per, desk, err := a2aFiles(st, all)
	if err != nil {
		t.Fatal(err)
	}
	if len(per) != 2 {
		t.Fatalf("Daytona hosts take no part: %v", per)
	}
	n := per["flocc-nimble"]
	if n.Name != "flocc-nimble" || n.Hosts["box2"].URL != "http://box2:7443" || n.Hosts["flocc-nimble"].Repos[0] != "nimblefox" {
		t.Fatalf("file: %+v", n)
	}
	if n.Accept[per["box2"].Token] != "host:box2" || n.Accept[desk.Token] != "owner" || n.Accept[n.Token] != "host:flocc-nimble" {
		t.Fatalf("accept: %v", n.Accept)
	}
	if desk.Name != "owner" || desk.Accept != nil || len(desk.Hosts) != 2 {
		t.Fatalf("desktop: %+v", desk)
	}
	again, _, _ := a2aFiles(st, all)
	if again["box2"].Token != per["box2"].Token {
		t.Fatal("tokens must be stable across ships")
	}
}

func TestA2AFilesRefuseARepoOnTwoHosts(t *testing.T) {
	all := []hosts.Host{a2aHost("a", "root@a", "web"), a2aHost("b", "root@b", "web")}
	_, _, err := a2aFiles(newMemStore(), all)
	if err == nil || !strings.Contains(err.Error(), "web") || !strings.Contains(err.Error(), "a") {
		t.Fatalf("err = %v", err)
	}
}
