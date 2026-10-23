package tailnet

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCheckOverTheFixtures(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		want    []string // substrings, one per missing item
	}{
		{"empty", []string{"tagOwners", "ssh", "test", "A2A"}},
		{"default", []string{"tagOwners", "from *", "ssh", "test", "A2A"}},
		{"applied-2026-10-22", nil},
		{"custom", []string{"tagOwners", "ssh", "test", "A2A"}},
	} {
		got, err := Check(fixture(t, tc.fixture))
		if err != nil {
			t.Fatalf("%s: %v", tc.fixture, err)
		}
		if len(got) != len(tc.want) {
			t.Fatalf("%s: missing = %q", tc.fixture, []string(got))
		}
		for i, w := range tc.want {
			if !strings.Contains(got[i], w) {
				t.Errorf("%s: missing[%d] = %q, want it to mention %q", tc.fixture, i, got[i], w)
			}
		}
	}
}

func TestCheckFlagsTheTagAsASource(t *testing.T) {
	for _, key := range []string{"grants", "acls"} {
		p := `{"` + key + `":[{"src":["tag:flocc-host"],"dst":["*"]}]}`
		got, err := Check([]byte(p))
		if err != nil || !slices.ContainsFunc(got, func(m string) bool { return strings.Contains(m, "source") }) {
			t.Errorf("%s: missing = %q, err = %v", key, []string(got), err)
		}
	}
}

func TestCheckNeedsTheWholeSSHRule(t *testing.T) {
	applied := string(fixture(t, "applied-2026-10-22"))
	for name, edit := range map[string]func(string) string{
		"check action": func(s string) string { return strings.Replace(s, `"accept", "src"`, `"check", "src"`, 1) },
		"no root": func(s string) string {
			return strings.Replace(s, `"users": ["root", "autogroup:nonroot"]`, `"users": ["autogroup:nonroot"]`, 1)
		},
		"no nonroot": func(s string) string {
			return strings.Replace(s, `"users": ["root", "autogroup:nonroot"]`, `"users": ["root"]`, 1)
		},
		"no deny": func(s string) string {
			return strings.Replace(s, `"deny": ["laptop:22", "laptop:443", "desktop:22", "desktop:443"]`, `"deny": []`, 1)
		},
		"no owner": func(s string) string { return strings.Replace(s, `"tag:flocc-host": ["autogroup:admin"]`, ``, 1) },
	} {
		got, err := Check([]byte(edit(applied)))
		if name == "no owner" {
			if err == nil && len(got) == 0 {
				t.Errorf("%s: passed", name)
			}
			continue
		}
		if err != nil || len(got) != 1 {
			t.Errorf("%s: missing = %q, err = %v", name, []string(got), err)
		}
	}
}

// a2aLess passes Check except for the A2A grant.
const a2aLess = `{"tagOwners":{"tag:flocc-host":["autogroup:admin"]},
"ssh":[{"action":"accept","src":["autogroup:member"],"dst":["tag:flocc-host"],"users":["root","autogroup:nonroot"]}],
"tests":[{"src":"tag:flocc-host","deny":["desktop:22"]}]}`

func withGrant(grant string) []byte {
	return []byte(strings.Replace(a2aLess, `"ssh"`, `"grants":[`+grant+`],"ssh"`, 1))
}

func TestCheckWantsTheA2AGrant(t *testing.T) {
	got, err := Check([]byte(a2aLess))
	if err != nil || len(got) != 1 || got[0] != needA2A {
		t.Errorf("missing = %q, err = %v", []string(got), err)
	}
}

func TestTheA2AGrantIsNotATagSource(t *testing.T) {
	got, err := Check(withGrant(`{"src":["tag:flocc-host"],"dst":["tag:flocc-host"],"ip":["tcp:7443"]}`))
	if err != nil || len(got) != 0 {
		t.Errorf("missing = %q, err = %v", []string(got), err)
	}
}

func TestAWiderTagGrantStillFails(t *testing.T) {
	for name, ip := range map[string]string{"wildcard": `"*"`, "extra port": `"tcp:7443","tcp:22"`} {
		got, err := Check(withGrant(`{"src":["tag:flocc-host"],"dst":["tag:flocc-host"],"ip":[` + ip + `]}`))
		if err != nil || !slices.Contains(got, hasTagSource) {
			t.Errorf("%s: missing = %q, err = %v", name, []string(got), err)
		}
	}
}

func TestAnAclWithTheA2AShapeIsNotTheA2AGrant(t *testing.T) {
	p := strings.Replace(a2aLess, `"ssh"`, `"acls":[{"src":["tag:flocc-host"],"dst":["tag:flocc-host"],"ip":["tcp:7443"]}],"ssh"`, 1)
	got, err := Check([]byte(p))
	if err != nil || !slices.Contains(got, needA2A) || !slices.Contains(got, hasTagSource) {
		t.Errorf("missing = %q, err = %v", []string(got), err)
	}
}

func TestMergeAddsTheA2AGrantAndDenies7443(t *testing.T) {
	out, added, err := Merge([]byte(`{}`), "owner@example.com", owners)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(added, needA2A) {
		t.Errorf("added = %q", []string(added))
	}
	if n := strings.Count(compact(t, out), `{"dst":["tag:flocc-host"],"ip":["tcp:7443"],"src":["tag:flocc-host"]}`); n != 1 {
		t.Errorf("A2A grant appears %d times:\n%s", n, out)
	}
	var doc struct{ Tests []struct{ Deny []string } }
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"desktop:7443", "laptop:7443"} {
		if !slices.Contains(doc.Tests[0].Deny, want) {
			t.Errorf("deny = %v, want %s", doc.Tests[0].Deny, want)
		}
	}
	if missing, err := Check(out); err != nil || len(missing) != 0 {
		t.Errorf("merged policy misses %q (%v)", []string(missing), err)
	}
	again, added, err := Merge(out, "owner@example.com", owners)
	if err != nil || len(added) != 0 || compact(t, again) != compact(t, out) {
		t.Errorf("second merge added %q (%v)", []string(added), err)
	}
}

func TestCheckRejectsNonJSON(t *testing.T) {
	if _, err := Check([]byte("// comment\n{}")); err == nil {
		t.Fatal("want an error for HuJSON input")
	}
}

var owners = []Device{
	{ID: "nSELF", Name: "desktop", IP: "100.64.0.2", Owner: "owner@example.com"},
	{ID: "nLAP", Name: "laptop", IP: "100.64.0.1", Owner: "owner@example.com"},
	{ID: "nFLOCC", Name: "flocc-nimble", IP: "100.64.0.7", Tags: []string{Tag}},
}

func TestMergeIntoTheDefaultPassesCheckAndNarrowsTheGrant(t *testing.T) {
	in := fixture(t, "default")
	out, added, err := Merge(in, "owner@example.com", owners)
	if err != nil {
		t.Fatal(err)
	}
	if missing, err := Check(out); err != nil || len(missing) != 0 {
		t.Fatalf("merged policy still misses %q (%v)\n%s", []string(missing), err, out)
	}
	if len(added) == 0 {
		t.Error("nothing reported as added")
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("not plain JSON: %v", err)
	}
	var grants []struct{ Src, Dst, IP []string }
	json.Unmarshal(doc["grants"], &grants)
	if len(grants) != 2 || !slices.Equal(grants[0].Src, []string{"autogroup:member"}) ||
		!slices.Equal(grants[0].Dst, []string{"*"}) || !slices.Equal(grants[0].IP, []string{"*"}) {
		t.Errorf("grants = %s", doc["grants"])
	}
	if !slices.ContainsFunc(added, func(m string) bool { return strings.Contains(m, "from *") }) {
		t.Errorf("added = %q, want the narrowing named", []string(added))
	}
	if !bytes.Contains(out, []byte("\n\t")) {
		t.Errorf("not indented with tabs:\n%s", out)
	}
}

func compact(t *testing.T, b []byte) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestMergeKeepsTheOwnersRulesAndAliasesInOrder(t *testing.T) {
	out, _, err := Merge(fixture(t, "custom"), "owner@example.com", owners)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, keep := range []string{`"src": ["group:eng"]`, `"dst": ["tag:db:5432"]`, `"db1": "100.64.0.9"`, `"laptop": "100.64.0.1"`, `"tag:db": [`} {
		if !strings.Contains(compact(t, out), strings.ReplaceAll(keep, " ", "")) {
			t.Errorf("lost %s:\n%s", keep, s)
		}
	}
	// top-level keys keep their order; the owner's ssh and tests entries stay first
	if !(strings.Index(s, `"groups"`) < strings.Index(s, `"tagOwners"`) &&
		strings.Index(s, `"tagOwners"`) < strings.Index(s, `"hosts"`) &&
		strings.Index(s, `"hosts"`) < strings.Index(s, `"acls"`)) {
		t.Errorf("top-level order changed:\n%s", s)
	}
	if strings.Index(s, `"group:eng"`) > strings.Index(s, `"tag:flocc-host"`) {
		t.Errorf("owner entries not first:\n%s", s)
	}
	if missing, _ := Check(out); len(missing) != 0 {
		t.Errorf("still missing %q", []string(missing))
	}
	if strings.Contains(s, `"desktop"`) == false {
		t.Errorf("owner device aliases not added:\n%s", s)
	}
}

func TestMergeOnAnAppliedPolicyAddsNothing(t *testing.T) {
	in := fixture(t, "applied-2026-10-22")
	out, added, err := Merge(in, "owner@example.com", owners)
	if err != nil || len(added) != 0 {
		t.Fatalf("added = %q, err = %v", []string(added), err)
	}
	if compact(t, out) != compact(t, in) {
		t.Errorf("changed:\n%s", out)
	}
}

func TestGenerateMatchesTheSpecShape(t *testing.T) {
	out := Generate("owner@example.com", owners)
	if missing, err := Check(out); err != nil || len(missing) != 0 {
		t.Fatalf("generated policy misses %q (%v)\n%s", []string(missing), err, out)
	}
	var doc struct {
		Hosts    map[string]string
		Grants   []struct{ Src []string }
		Tests    []struct{ Deny []string }
		SSHTests []struct {
			Src    string
			Accept []string
		} `json:"sshTests"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Hosts["laptop"] != "100.64.0.1" || doc.Hosts["desktop"] != "100.64.0.2" || len(doc.Hosts) != 2 {
		t.Errorf("hosts = %v", doc.Hosts)
	}
	if len(doc.Grants) != 2 || doc.Grants[0].Src[0] != "autogroup:member" || doc.Grants[1].Src[0] != Tag {
		t.Errorf("grants = %v", doc.Grants)
	}
	if !slices.Equal(doc.Tests[0].Deny, []string{"desktop:22", "desktop:443", "desktop:7443", "laptop:22", "laptop:443", "laptop:7443"}) {
		t.Errorf("deny = %v", doc.Tests[0].Deny)
	}
	if doc.SSHTests[0].Src != "owner@example.com" || doc.SSHTests[0].Accept[0] != "root" {
		t.Errorf("sshTests = %v", doc.SSHTests)
	}
}

func TestDevicesSplitsTaggedFromOwnerDevices(t *testing.T) {
	got, err := Devices(fixture(t, "status"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Device{
		{ID: "nSELF", Name: "desktop", IP: "100.64.0.2", Owner: "owner@example.com"},
		{ID: "nFLOCC", Name: "flocc-nimble", IP: "100.64.0.7", Tags: []string{Tag}},
		{ID: "nLAP", Name: "laptop", IP: "100.64.0.1", Owner: "owner@example.com"},
	}
	if len(got) != len(want) {
		t.Fatalf("devices = %+v", got)
	}
	for i := range want {
		if got[i].ID != want[i].ID || got[i].Name != want[i].Name || got[i].IP != want[i].IP ||
			got[i].Owner != want[i].Owner || !slices.Equal(got[i].Tags, want[i].Tags) {
			t.Errorf("devices[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestCheckFlagsAWildcardSourceInAclsToo(t *testing.T) {
	got, err := Check([]byte(`{"acls":[{"action":"accept","src":["*"],"dst":["*:*"]}]}`))
	if err != nil || !slices.ContainsFunc(got, func(m string) bool { return strings.Contains(m, "from *") }) {
		t.Errorf("missing = %q, err = %v", []string(got), err)
	}
}

func TestMergeKeepsTheOtherSourcesWhenNarrowingTheWildcard(t *testing.T) {
	in := `{"acls":[{"action":"accept","src":["group:a","*"],"dst":["*:*"]}]}`
	out, _, err := Merge([]byte(in), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compact(t, out), `"src":["group:a","autogroup:member"]`) {
		t.Errorf("src not narrowed in place:\n%s", out)
	}
}
