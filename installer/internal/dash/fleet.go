package dash

import (
	"fmt"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
	"github.com/NovusEdge/flocc/installer/internal/ui/theme"
)

func (m *Model) fleetView() string {
	iw, _ := theme.Inner(m.w, m.h)
	var b []string
	for i, r := range m.rows {
		b = append(b, m.th.Row(i == m.sel, fmt.Sprintf("%-8s %s", saved.Sanitize(r.claim.Issue), saved.Sanitize(r.host))))
	}
	return m.th.Pane("issues", "", strings.Join(b, "\n"), iw, m.bodyHeight())
}
