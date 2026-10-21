package steps

import (
	"fmt"
	"strings"

	"github.com/NovusEdge/flocc/deploy"
)

// CardinalUnits are cardinal's three user units, keyed by file name. The timer's window is the two
// hours from renewHour: a laptop asleep through one tick still gets three more.
func CardinalUnits(renewHour int) map[string][]byte {
	out := map[string][]byte{}
	for _, u := range deploy.CardinalUnits {
		b, _ := deploy.Cardinal.ReadFile("cardinal/" + u)
		if u == "cardinal-renew.timer" {
			hours := fmt.Sprintf("%02d,%02d", renewHour%24, (renewHour+1)%24)
			b = []byte(strings.ReplaceAll(string(b), "{{hours}}", hours))
		}
		out[u] = b
	}
	return out
}

// CardinalBrief is the brief as cardinal reads it in ~/CLAUDE.md.
func CardinalBrief(user, machine string) []byte {
	b, _ := deploy.Cardinal.ReadFile("cardinal/CLAUDE.md")
	return []byte(strings.NewReplacer("{{user}}", user, "{{machine}}", machine).Replace(string(b)))
}
