package ui

// Kind is what an event draws.
type Kind string

const (
	KGroup Kind = "group" // a section heading; later steps belong to it
	KStep  Kind = "step"  // a row; State says whether it still runs
	KLine  Kind = "line"  // a command or output line of the step ID, for the tail and the log
	KNote  Kind = "note"
	KTodo  Kind = "todo" // collected into the "still yours to do" box
	KTable Kind = "table"
	KPlan  Kind = "plan" // the groups to come, one per line of Text, for the full-screen rail
	KLog   Kind = "log"  // Text is the log file the record names at the end
	KData  Kind = "data" // machine data for the desktop under Name (a host summary); never drawn
)

type State string

const (
	Running State = "running"
	Done    State = "done"
	Pending State = "pending" // did what it could; the owner has the rest
	Skipped State = "skipped"
	Failed  State = "failed"
)

// Event is one thing the session draws. A step sends one event per change under the same ID;
// the last one has a State other than Running. The wire format in wire.go is this struct as JSON.
type Event struct {
	Kind   Kind   `json:"type"`
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Detail string `json:"detail,omitempty"`
	Text   string `json:"text,omitempty"` // a line's text, a note's body, a to-do
	State  State  `json:"state,omitempty"`
	N      int    `json:"n,omitempty"` // progress: N of Total
	Total  int    `json:"total,omitempty"`
	Err    string `json:"err,omitempty"`
	Table  *Table `json:"table,omitempty"`
}

// Table is a titled grid; renderers pick their own borders.
type Table struct {
	Title   string     `json:"title,omitempty"`
	Headers []string   `json:"headers"`
	Rows    [][]string `json:"rows"`
}
