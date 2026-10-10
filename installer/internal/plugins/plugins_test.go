package plugins

import (
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "plugins.json")
	l := List{Marketplaces: []Marketplace{{Name: "NovusEdge", Archive: "mp-NovusEdge.tar", Commit: "abc"}, {Name: "curt", GitHub: "o/curt"}},
		Plugins: []string{"docket@NovusEdge", "curt@curt"}}
	if err := l.Write(p); err != nil {
		t.Fatal(err)
	}
	back, err := Load(p)
	if err != nil || len(back.Plugins) != 2 || back.Marketplaces[0].Archive != "mp-NovusEdge.tar" || back.Marketplaces[0].Commit != "abc" {
		t.Fatalf("%+v %v", back, err)
	}
}

func TestLoadMissingIsEmpty(t *testing.T) {
	l, err := Load(filepath.Join(t.TempDir(), "none.json"))
	if err != nil || len(l.Plugins) != 0 {
		t.Fatalf("%+v %v", l, err)
	}
}
