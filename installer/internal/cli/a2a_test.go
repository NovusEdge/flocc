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
	ips := map[string]string{"flocc-nimble": "100.69.173.63"}
	per, desk, err := a2aFiles(st, all, ips)
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
	// The host's own name resolves to 127.0.1.1 there, while its server binds the tailnet address.
	if u := n.Hosts["flocc-nimble"].URL; u != "http://100.69.173.63:7443" {
		t.Fatalf("url %s must carry the tailnet address", u)
	}
	if n.Accept[tokenID(per["box2"].Token)] != "host:box2" || n.Accept[tokenID(desk.Token)] != "owner" || n.Accept[tokenID(n.Token)] != "host:flocc-nimble" {
		t.Fatalf("accept: %v", n.Accept)
	}
	if _, clear := n.Accept[desk.Token]; clear {
		t.Fatal("accept must not hold the owner's token in clear text")
	}
	if tokenID("abc") != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatal("tokenID is lowercase hex SHA-256")
	}
	if desk.Name != "owner" || desk.Accept != nil || len(desk.Hosts) != 2 {
		t.Fatalf("desktop: %+v", desk)
	}
	again, _, _ := a2aFiles(st, all, ips)
	if again["box2"].Token != per["box2"].Token {
		t.Fatal("tokens must be stable across ships")
	}
}

func TestA2AFilesRefuseARepoOnTwoHosts(t *testing.T) {
	all := []hosts.Host{a2aHost("a", "root@a", "web"), a2aHost("b", "root@b", "web")}
	_, _, err := a2aFiles(newMemStore(), all, nil)
	if err == nil || !strings.Contains(err.Error(), "web") || !strings.Contains(err.Error(), "a") {
		t.Fatalf("err = %v", err)
	}
}
