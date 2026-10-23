package tailnet

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"
)

// Preamble goes on the first line of a policy flocc prints: the JSON the API returns has lost any
// comments of the original HuJSON file, and Merge works on that JSON.
const Preamble = "// Merged by flocc from the JSON the Tailscale API returns; comments in the file you pasted last are not kept.\n"

// Device is a node from tailscale status --json. ID is the node's stable ID ("n…"), which the
// device API takes. Owner is empty for a tagged node.
type Device struct {
	ID, Name, IP string
	Tags         []string
	Owner        string
}

// Missing lists what the policy lacks, in words for the step detail.
type Missing []string

const (
	needOwners   = "tagOwners entry for " + Tag
	needSSH      = "ssh accept rule from autogroup:member to " + Tag + " for root and autogroup:nonroot"
	needDeny     = "test that " + Tag + " cannot reach the owner's devices"
	hasTagSource = Tag + " is the source of a grant or acl; remove it so hosts reach nothing"
	hasWildcard  = "a grant from * lets tagged hosts reach your devices"
	needA2A      = "grant from " + Tag + " to " + Tag + " on tcp:7443, for A2A between hosts"
	memberGroup  = "autogroup:member"
	a2aPort      = "tcp:7443"
)

type sshRule struct {
	Action string   `json:"action"`
	Src    []string `json:"src"`
	Dst    []string `json:"dst"`
	Users  []string `json:"users"`
}

type accessTest struct {
	Src  string   `json:"src"`
	Deny []string `json:"deny"`
}

type srcRule struct {
	Src []string `json:"src"`
	Dst []string `json:"dst"`
	IP  []string `json:"ip"`
}

// isA2A is the one rule with the tag as its source that the policy may hold: hosts reach each
// other's A2A port and nothing else.
func (r srcRule) isA2A() bool {
	return slices.Equal(r.Src, []string{Tag}) && slices.Equal(r.Dst, []string{Tag}) && slices.Equal(r.IP, []string{a2aPort})
}

func (r sshRule) admitsHosts() bool {
	return r.Action == "accept" && slices.Contains(r.Src, memberGroup) &&
		slices.Contains(r.Dst, Tag) && slices.Contains(r.Users, "root") &&
		slices.Contains(r.Users, "autogroup:nonroot")
}

func (t accessTest) deniesHosts() bool { return t.Src == Tag && len(t.Deny) > 0 }

type member struct {
	key string
	val json.RawMessage
}

// object keeps key order, which a map would lose and a reader of the pasted file would notice.
type object []member

func parseObject(b []byte) (object, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("the policy is not a plain JSON object (comments and trailing commas are not accepted): %v", err)
	}
	var o object
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		o = append(o, member{k.(string), v})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return o, nil
}

func (o object) get(key string) json.RawMessage {
	for _, m := range o {
		if m.key == key {
			if string(m.val) == "null" {
				return nil
			}
			return m.val
		}
	}
	return nil
}

func (o object) set(key string, val any) object {
	raw, _ := json.Marshal(val)
	for i, m := range o {
		if m.key == key {
			o[i].val = raw
			return o
		}
	}
	return append(o, member{key, raw})
}

func (o object) bytes() []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(m.key)
		b.Write(k)
		b.WriteByte(':')
		b.Write(m.val)
	}
	b.WriteByte('}')
	return b.Bytes()
}

func decodeList[T any](raw json.RawMessage, what string) ([]T, error) {
	if raw == nil {
		return nil, nil
	}
	var out []T
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("policy %s: %w", what, err)
	}
	return out, nil
}

type facts struct {
	top                 object
	owners              object
	hasOwner, tagSource bool
	wildSource, hasA2A  bool
	ssh                 []sshRule
	tests               []accessTest
}

func read(policy []byte) (facts, error) {
	var f facts
	var err error
	if f.top, err = parseObject(policy); err != nil {
		return f, err
	}
	if raw := f.top.get("tagOwners"); raw != nil {
		if f.owners, err = parseObject(raw); err != nil {
			return f, fmt.Errorf("policy tagOwners: %w", err)
		}
		f.hasOwner = f.owners.get(Tag) != nil
	}
	for _, key := range []string{"grants", "acls"} {
		rules, err := decodeList[srcRule](f.top.get(key), key)
		if err != nil {
			return f, err
		}
		for _, r := range rules {
			a2a := key == "grants" && r.isA2A()
			f.hasA2A = f.hasA2A || a2a
			f.tagSource = f.tagSource || (slices.Contains(r.Src, Tag) && !a2a)
			f.wildSource = f.wildSource || slices.Contains(r.Src, "*")
		}
	}
	if f.ssh, err = decodeList[sshRule](f.top.get("ssh"), "ssh"); err != nil {
		return f, err
	}
	f.tests, err = decodeList[accessTest](f.top.get("tests"), "tests")
	return f, err
}

func (f facts) missing() Missing {
	var m Missing
	if !f.hasOwner {
		m = append(m, needOwners)
	}
	if f.tagSource {
		m = append(m, hasTagSource)
	}
	if f.wildSource {
		m = append(m, hasWildcard)
	}
	if !slices.ContainsFunc(f.ssh, sshRule.admitsHosts) {
		m = append(m, needSSH)
	}
	if !slices.ContainsFunc(f.tests, accessTest.deniesHosts) {
		m = append(m, needDeny)
	}
	if !f.hasA2A {
		m = append(m, needA2A)
	}
	return m
}

// Check reports what the policy lacks for hosts tagged Tag to be reachable and to reach nothing.
// A src of "*" counts against the policy: Tailscale's default grant from * covers tagged nodes too.
func Check(policy []byte) (Missing, error) {
	f, err := read(policy)
	if err != nil {
		return nil, err
	}
	return f.missing(), nil
}

func alias(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 'a' - 'A'
		}
		return '-'
	}, name)
}

func ownerDevices(devices []Device) []Device {
	var out []Device
	for _, d := range devices {
		if len(d.Tags) == 0 && d.IP != "" {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return alias(out[i].Name) < alias(out[j].Name) })
	return out
}

func appendRaw(top object, key string, entry any) (object, error) {
	var list []json.RawMessage
	if raw := top.get(key); raw != nil {
		if err := json.Unmarshal(raw, &list); err != nil {
			return top, fmt.Errorf("policy %s: %w", key, err)
		}
	}
	e, _ := json.Marshal(entry)
	return top.set(key, append(list, e)), nil
}

// Merge adds only what Check reports missing, plus an sshTests entry that owner can reach the tag
// as root, and keeps every other key and entry in order. A grant or acl with the tag as its source
// is reported by Check but never rewritten. The second result lists what was added.
func Merge(policy []byte, owner string, devices []Device) ([]byte, Missing, error) {
	f, err := read(policy)
	if err != nil {
		return nil, nil, err
	}
	top := f.top
	var added Missing
	if f.wildSource {
		for _, key := range []string{"grants", "acls"} {
			if top, err = narrowWildcard(top, key); err != nil {
				return nil, nil, err
			}
		}
		added = append(added, "src * narrowed to "+memberGroup+" ("+hasWildcard+")")
	}
	if !f.hasOwner {
		owners := f.owners.set(Tag, []string{"autogroup:admin"})
		top = top.set("tagOwners", json.RawMessage(owners.bytes()))
		added = append(added, needOwners)
	}
	if !slices.ContainsFunc(f.ssh, sshRule.admitsHosts) {
		rule := sshRule{"accept", []string{"autogroup:member"}, []string{Tag}, []string{"root", "autogroup:nonroot"}}
		if top, err = appendRaw(top, "ssh", rule); err != nil {
			return nil, nil, err
		}
		added = append(added, needSSH)
	}
	if !f.hasA2A {
		grant := map[string]any{"src": []string{Tag}, "dst": []string{Tag}, "ip": []string{a2aPort}}
		if top, err = appendRaw(top, "grants", grant); err != nil {
			return nil, nil, err
		}
		added = append(added, needA2A)
	}
	if !slices.ContainsFunc(f.tests, accessTest.deniesHosts) {
		own := ownerDevices(devices)
		if len(own) > 0 {
			hosts := object(nil)
			if raw := top.get("hosts"); raw != nil {
				if hosts, err = parseObject(raw); err != nil {
					return nil, nil, fmt.Errorf("policy hosts: %w", err)
				}
			}
			var deny []string
			for _, d := range own {
				a := alias(d.Name)
				if hosts.get(a) == nil {
					hosts = hosts.set(a, d.IP)
				}
				deny = append(deny, a+":22", a+":443", a+":7443")
			}
			top = top.set("hosts", json.RawMessage(hosts.bytes()))
			if top, err = appendRaw(top, "tests", accessTest{Tag, deny}); err != nil {
				return nil, nil, err
			}
			added = append(added, needDeny)
		}
	}
	if owner != "" && !hasSSHTest(top) {
		entry := map[string]any{"src": owner, "dst": []string{Tag}, "accept": []string{"root"}}
		if top, err = appendRaw(top, "sshTests", entry); err != nil {
			return nil, nil, err
		}
		added = append(added, "sshTests entry that "+owner+" can ssh to "+Tag+" as root")
	}
	var out bytes.Buffer
	if err := json.Indent(&out, top.bytes(), "", "\t"); err != nil {
		return nil, nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), added, nil
}

// narrowWildcard replaces a "*" in the src of each rule under key with autogroup:member, which
// excludes tagged nodes, and leaves every other field and source where it was.
func narrowWildcard(top object, key string) (object, error) {
	raw := top.get(key)
	if raw == nil {
		return top, nil
	}
	var rules []json.RawMessage
	if err := json.Unmarshal(raw, &rules); err != nil {
		return top, fmt.Errorf("policy %s: %w", key, err)
	}
	for i, r := range rules {
		rule, err := parseObject(r)
		if err != nil {
			return top, fmt.Errorf("policy %s: %w", key, err)
		}
		var src []string
		if json.Unmarshal(rule.get("src"), &src) != nil || !slices.Contains(src, "*") {
			continue
		}
		var narrowed []string
		for _, s := range src {
			if s == "*" {
				s = memberGroup
			}
			if !slices.Contains(narrowed, s) {
				narrowed = append(narrowed, s)
			}
		}
		rules[i] = rule.set("src", narrowed).bytes()
	}
	return top.set(key, rules), nil
}

func hasSSHTest(top object) bool {
	type sshTest struct {
		Dst []string `json:"dst"`
	}
	tests, _ := decodeList[sshTest](top.get("sshTests"), "sshTests")
	return slices.ContainsFunc(tests, func(t sshTest) bool { return slices.Contains(t.Dst, Tag) })
}

// Generate is the policy for a tailnet that has nothing else: every member reaches everything, SSH
// as before, and the tag reaches nothing.
func Generate(owner string, devices []Device) []byte {
	base := object{}.
		set("tagOwners", map[string]any{}).
		set("hosts", map[string]any{}).
		set("grants", []any{map[string]any{"src": []string{"autogroup:member"}, "dst": []string{"*"}, "ip": []string{"*"}}}).
		set("ssh", []any{sshRule{"check", []string{"autogroup:member"}, []string{"autogroup:self"}, []string{"autogroup:nonroot", "root"}}})
	out, _, err := Merge(base.bytes(), owner, devices)
	if err != nil {
		panic(err) // the base above is valid by construction
	}
	return out
}

// Devices parses tailscale status --json: this node first, then the peers by name.
func Devices(statusJSON []byte) ([]Device, error) {
	type node struct {
		ID           string
		HostName     string
		TailscaleIPs []string
		Tags         []string
		UserID       int64
	}
	var st struct {
		Self node
		Peer map[string]node
		User map[string]struct{ LoginName string }
	}
	if err := json.Unmarshal(statusJSON, &st); err != nil {
		return nil, fmt.Errorf("tailscale status: %w", err)
	}
	conv := func(n node) Device {
		d := Device{ID: n.ID, Name: n.HostName, Tags: n.Tags}
		for _, s := range n.TailscaleIPs {
			if a, err := netip.ParseAddr(s); err == nil && a.Is4() {
				d.IP = s
				break
			}
		}
		if len(n.Tags) == 0 {
			d.Owner = st.User[fmt.Sprint(n.UserID)].LoginName
		}
		return d
	}
	peers := make([]Device, 0, len(st.Peer))
	for _, n := range st.Peer {
		peers = append(peers, conv(n))
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].Name < peers[j].Name })
	return append([]Device{conv(st.Self)}, peers...), nil
}
