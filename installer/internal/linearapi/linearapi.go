// Package linearapi is the part of Linear's GraphQL API the desktop uses: checking a key, and
// the digest issue and labels host add makes.
package linearapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const DefaultURL = "https://api.linear.app/graphql"

var ErrNotFound = errors.New("Linear has no such issue")

type Client struct {
	URL, Key string
	HTTP     *http.Client
}

func New(key string) *Client {
	return &Client{URL: DefaultURL, Key: key, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

type Team struct{ ID, Key, Name string }

type Viewer struct {
	Name, Email string
	Teams       []Team
}

type Issue struct{ ID, Key, Title, StateType, URL string }

func (i Issue) Open() bool { return i.StateType != "completed" && i.StateType != "canceled" }

// ErrKeyRejected and ErrUnavailable classify an *Error with errors.Is. A rejected key is the
// owner's to replace; an unavailable Linear (no answer, 5xx, rate limit, unreadable reply) is
// worth retrying, and says nothing about the key.
var (
	ErrKeyRejected = errors.New("Linear rejected the key")
	ErrUnavailable = errors.New("Linear is unavailable")
)

// Error is a failed call: Status is the HTTP status (0 when no reply arrived) and Code the first
// GraphQL extensions.code, such as AUTHENTICATION_ERROR, FORBIDDEN or RATELIMITED.
type Error struct {
	Status    int
	Code      string
	Message   string
	malformed bool
	cause     error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.cause }

func (e *Error) unavailable() bool {
	return e.Status == 0 || e.Status >= 500 || e.Status == 429 || e.Code == "RATELIMITED" || e.malformed
}

// The two classes are disjoint: a 401 from a proxy's HTML page, or an auth code on a 5xx, says
// nothing reliable about the key, so unavailable wins.
func (e *Error) Is(target error) bool {
	switch target {
	case ErrKeyRejected:
		return !e.unavailable() && (e.Code == "AUTHENTICATION_ERROR" || e.Code == "FORBIDDEN" || e.Status == 401 || e.Status == 403)
	case ErrUnavailable:
		return e.unavailable()
	}
	return false
}

const maxReply = 4 << 20

// do posts one query. Linear reports a bad key and rate limits as HTTP 400 with a JSON error
// list, so the error list decides before the status does.
func (c *Client) do(ctx context.Context, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.Key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return &Error{Message: "Linear did not answer: " + err.Error(), cause: err}
	}
	defer resp.Body.Close()
	status := resp.StatusCode
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReply+1))
	if err != nil {
		return &Error{Status: status, Message: fmt.Sprintf("Linear HTTP %d: reading the reply: %v", status, err), malformed: true, cause: err}
	}
	if len(raw) > maxReply {
		return &Error{Status: status, Message: fmt.Sprintf("Linear HTTP %d: reply over %d MiB", status, maxReply>>20), malformed: true}
	}
	var env struct {
		Data   json.RawMessage
		Errors []struct {
			Message    string
			Extensions struct{ Code string }
		}
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return &Error{Status: status, Message: fmt.Sprintf("Linear HTTP %d: unreadable reply", status), malformed: true}
	}
	if len(env.Errors) > 0 {
		msgs := make([]string, len(env.Errors))
		var code string
		for i, e := range env.Errors {
			msgs[i] = e.Message
			if code == "" {
				code = e.Extensions.Code
			}
		}
		return &Error{Status: status, Code: code, Message: "Linear: " + strings.Join(msgs, "; ")}
	}
	if status < 200 || status > 299 {
		return &Error{Status: status, Message: fmt.Sprintf("Linear HTTP %d", status)}
	}
	if len(env.Data) == 0 {
		return &Error{Status: status, Message: fmt.Sprintf("Linear HTTP %d: reply has no data", status), malformed: true}
	}
	return json.Unmarshal(env.Data, out)
}

type issueNode struct {
	ID, Identifier, Title, URL string
	State                      struct{ Type string }
}

func (n issueNode) issue() Issue {
	return Issue{ID: n.ID, Key: n.Identifier, Title: n.Title, StateType: n.State.Type, URL: n.URL}
}

const issueFields = "id identifier title url state { type }"

var issueKey = regexp.MustCompile(`^([A-Z][A-Z0-9]*)-([0-9]+)$`)

// maxPage is Linear's cap on a connection's first argument.
const maxPage = 250

// IssuesByKey returns the issues among keys that Linear knows, one query per maxPage keys. Keys
// that are not TEAM-NUMBER are skipped and repeats are asked once; an unknown key is simply absent.
func (c *Client) IssuesByKey(ctx context.Context, keys []string) ([]Issue, error) {
	var or []map[string]any
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			continue
		}
		seen[k] = true
		m := issueKey.FindStringSubmatch(k)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		or = append(or, map[string]any{"team": map[string]any{"key": map[string]any{"eq": m[1]}}, "number": map[string]any{"eq": n}})
	}
	if len(or) == 0 {
		return nil, nil
	}
	var issues []Issue
	for chunk := range slices.Chunk(or, maxPage) {
		var out struct{ Issues struct{ Nodes []issueNode } }
		err := c.do(ctx, `query($filter: IssueFilter!, $n: Int!) { issues(filter: $filter, first: $n) { nodes { `+issueFields+` } } }`,
			map[string]any{"filter": map[string]any{"or": chunk}, "n": len(chunk)}, &out)
		if err != nil {
			return nil, err
		}
		for _, n := range out.Issues.Nodes {
			issues = append(issues, n.issue())
		}
	}
	return issues, nil
}

func (c *Client) Viewer(ctx context.Context) (Viewer, error) {
	var out struct {
		Viewer struct{ Name, Email string }
		Teams  struct{ Nodes []Team }
	}
	err := c.do(ctx, "query { viewer { name email } teams(first: 100) { nodes { id key name } } }", nil, &out)
	return Viewer{Name: out.Viewer.Name, Email: out.Viewer.Email, Teams: out.Teams.Nodes}, err
}

func (c *Client) Issue(ctx context.Context, key string) (Issue, error) {
	var out struct{ Issue issueNode }
	err := c.do(ctx, "query($id: String!) { issue(id: $id) { "+issueFields+" } }", map[string]any{"id": key}, &out)
	// Linear reports a missing issue only in the message text.
	if err != nil && strings.Contains(err.Error(), "Entity not found") {
		err = fmt.Errorf("%s: %w", key, ErrNotFound)
	}
	return out.Issue.issue(), err
}

func (c *Client) SetTitle(ctx context.Context, id, title string) error {
	var out struct{ IssueUpdate struct{ Success bool } }
	err := c.do(ctx, `mutation($id: String!, $title: String!) {
		issueUpdate(id: $id, input: { title: $title }) { success } }`,
		map[string]any{"id": id, "title": title}, &out)
	if err == nil && !out.IssueUpdate.Success {
		err = errors.New("Linear did not apply the update")
	}
	return err
}

// FindOpenIssue finds a team issue with exactly this title that is not completed or canceled.
func (c *Client) FindOpenIssue(ctx context.Context, teamKey, title string) (Issue, bool, error) {
	var out struct{ Issues struct{ Nodes []issueNode } }
	err := c.do(ctx, `query($team: String!, $title: String!) { issues(first: 10, filter: {
		team: { key: { eq: $team } }, title: { eq: $title },
		state: { type: { nin: ["completed", "canceled"] } } }) { nodes { `+issueFields+` } } }`,
		map[string]any{"team": teamKey, "title": title}, &out)
	if err != nil || len(out.Issues.Nodes) == 0 {
		return Issue{}, false, err
	}
	return out.Issues.Nodes[0].issue(), true, nil
}

func (c *Client) CreateIssue(ctx context.Context, teamID, title, description string) (Issue, error) {
	var out struct{ IssueCreate struct{ Issue issueNode } }
	err := c.do(ctx, `mutation($team: String!, $title: String!, $desc: String!) {
		issueCreate(input: { teamId: $team, title: $title, description: $desc }) { issue { `+issueFields+` } } }`,
		map[string]any{"team": teamID, "title": title, "desc": description}, &out)
	return out.IssueCreate.Issue.issue(), err
}

// MissingLabels is the Go form of linear.missing_labels on the host: a name counts when a
// workspace label or one of this team's has it.
func (c *Client) MissingLabels(ctx context.Context, teamKey string, names []string) ([]string, error) {
	usable := map[string]bool{}
	// Chunked so a long name list cannot push a label past the 250-node page.
	for i := 0; i < max(len(names), 1); i += 100 {
		var out struct {
			Teams       struct{ Nodes []struct{ ID string } }
			IssueLabels struct {
				Nodes []struct {
					Name string
					Team *struct{ Key string }
				}
			}
		}
		err := c.do(ctx, `query($key: String!, $names: [String!]) {
			teams(filter: { key: { eq: $key } }) { nodes { id } }
			issueLabels(first: 250, filter: { name: { in: $names } }) { nodes { name team { key } } } }`,
			map[string]any{"key": teamKey, "names": names[i:min(i+100, len(names))]}, &out)
		if err != nil {
			return nil, err
		}
		if len(out.Teams.Nodes) == 0 {
			return nil, fmt.Errorf("team %s does not exist or the key cannot see it", teamKey)
		}
		for _, n := range out.IssueLabels.Nodes {
			if n.Team == nil || n.Team.Key == teamKey {
				usable[n.Name] = true
			}
		}
	}
	var missing []string
	for _, n := range names {
		if !usable[n] {
			missing = append(missing, n)
		}
	}
	return missing, nil
}

func (c *Client) CreateLabel(ctx context.Context, teamID, name string) error {
	var out struct{ IssueLabelCreate struct{ Success bool } }
	err := c.do(ctx, `mutation($team: String!, $name: String!) {
		issueLabelCreate(input: { teamId: $team, name: $name }) { success } }`,
		map[string]any{"team": teamID, "name": name}, &out)
	if err == nil && !out.IssueLabelCreate.Success {
		err = fmt.Errorf("Linear did not create label %s", name)
	}
	return err
}

// CanWrite proves the key may write by setting issueKey's title to the title it already has; only
// updatedAt moves. A key made with read-only permissions passes every read, then fails on the
// service's first claim.
func CanWrite(ctx context.Context, c *Client, issueKey string) error {
	is, err := c.Issue(ctx, issueKey)
	if err != nil {
		return explain(err, "was refused by Linear")
	}
	if is.ID == "" {
		return fmt.Errorf("could not check Linear: it returned no issue for %s", issueKey)
	}
	if err := c.SetTitle(ctx, is.ID, is.Title); err != nil {
		return explain(err, "cannot write to "+issueKey)
	}
	return nil
}

// explain tells the owner to replace the key only when Linear refused it; an outage or a rate
// limit must not send them to remake a good key.
func explain(err error, rejected string) error {
	switch {
	case errors.Is(err, ErrKeyRejected):
		return fmt.Errorf("the key %s (%w); make one with Full access", rejected, err)
	case errors.Is(err, ErrNotFound):
		return err
	}
	return fmt.Errorf("could not check Linear: %w", err)
}

// CheckKey reads the viewer and its teams; every team key in teams must be visible, and with
// none named any team will do. CanWrite proves write access.
func CheckKey(ctx context.Context, c *Client, teams []string) (Viewer, error) {
	v, err := c.Viewer(ctx)
	if err != nil {
		return v, explain(err, "was refused by Linear")
	}
	var seen []string
	for _, t := range v.Teams {
		seen = append(seen, t.Key)
	}
	if len(seen) == 0 {
		return v, fmt.Errorf("the key sees no Linear team")
	}
	for _, want := range teams {
		if !slices.Contains(seen, want) {
			return v, fmt.Errorf("the key cannot see team %s (it sees %s); make one that can", want, strings.Join(seen, ", "))
		}
	}
	return v, nil
}
