import json
import urllib.error
import urllib.request
from collections.abc import Callable
from dataclasses import dataclass

API = "https://api.linear.app/graphql"
BOT_PREFIX = "🤖 linear-agents:"
Transport = Callable[[str, dict], dict]

_ISSUE_FIELDS = (
    "id identifier title description url state { type } labels { nodes { name } }"
    " project { name } team { id }"
)


class LinearError(RuntimeError):
    pass


@dataclass(frozen=True)
class Issue:
    id: str
    key: str
    title: str
    description: str
    url: str
    state_type: str
    labels: tuple[str, ...]
    project: str | None
    team_id: str


@dataclass(frozen=True)
class Comment:
    id: str
    body: str
    created_at: str
    mine: bool = False


def http_transport(api_key: str) -> Transport:
    def post(query: str, variables: dict) -> dict:
        req = urllib.request.Request(
            API,
            data=json.dumps({"query": query, "variables": variables}).encode(),
            headers={"Authorization": api_key, "Content-Type": "application/json"},
        )
        try:
            with urllib.request.urlopen(req, timeout=30) as resp:
                body = json.loads(resp.read())
        except urllib.error.HTTPError as exc:
            # Linear reports rate limits and query errors as HTTP 400 with a JSON body.
            try:
                body = json.loads(exc.read())
            except ValueError:
                raise LinearError(f"HTTP {exc.code}") from exc
        # Connection failures, timeouts and a 200 that is not JSON: the tick counts only
        # LinearError toward the linear-api page.
        except (OSError, ValueError) as exc:
            raise LinearError(f"request failed: {exc}") from exc
        if body.get("errors"):
            raise LinearError("; ".join(e.get("message", "?") for e in body["errors"]))
        return body["data"]

    return post


def _issue(node: dict) -> Issue:
    return Issue(
        id=node["id"],
        key=node["identifier"],
        title=node["title"],
        description=node.get("description") or "",
        url=node["url"],
        state_type=node["state"]["type"],
        labels=tuple(n["name"] for n in node["labels"]["nodes"]),
        project=(node.get("project") or {}).get("name"),
        team_id=node["team"]["id"],
    )


class Linear:
    def __init__(self, post: Transport):
        self._post = post

    def ready_issues(self, team_key: str, label: str) -> list[Issue]:
        query = f"""query($team: String!, $label: String!, $after: String) {{
          viewer {{ assignedIssues(first: 50, after: $after, filter: {{
            team: {{ key: {{ eq: $team }} }},
            labels: {{ name: {{ eq: $label }} }},
            state: {{ type: {{ eq: "unstarted" }} }} }}) {{
              nodes {{ {_ISSUE_FIELDS} }}
              pageInfo {{ hasNextPage endCursor }} }} }} }}"""
        out: list[Issue] = []
        after = None
        while True:
            data = self._post(query, {"team": team_key, "label": label, "after": after})
            page = data["viewer"]["assignedIssues"]
            out += [_issue(n) for n in page["nodes"]]
            if not page["pageInfo"]["hasNextPage"]:
                return out
            after = page["pageInfo"]["endCursor"]

    def issue(self, key: str) -> Issue:
        data = self._post(
            f"query($id: String!) {{ issue(id: $id) {{ {_ISSUE_FIELDS} }} }}", {"id": key}
        )
        return _issue(data["issue"])

    def state_types(self, keys) -> dict[str, str]:
        query = "query($id: String!) { issue(id: $id) { identifier state { type } } }"
        out = {}
        for key in keys:
            issue = self._post(query, {"id": key})["issue"]
            out[key] = issue["state"]["type"]
        return out

    def start(self, issue: Issue) -> None:
        self._move(issue, "started")

    def unstart(self, issue: Issue) -> None:
        self._move(issue, "unstarted")

    def _move(self, issue: Issue, state_type: str) -> None:
        states = self._post(
            """query($team: String!, $type: String!) { team(id: $team) {
                 states(filter: { type: { eq: $type } }) { nodes { id position } } } }""",
            {"team": issue.team_id, "type": state_type},
        )["team"]["states"]["nodes"]
        if not states:
            raise LinearError(f"team of {issue.key} has no {state_type} state")
        first = min(states, key=lambda s: s["position"])
        self._post(
            "mutation($id: String!, $stateId: String!) {"
            " issueUpdate(id: $id, input: { stateId: $stateId }) { success } }",
            {"id": issue.id, "stateId": first["id"]},
        )

    def comment(self, issue_id: str, text: str) -> None:
        self._post(
            "mutation($id: String!, $body: String!) {"
            " commentCreate(input: { issueId: $id, body: $body }) { success } }",
            {"id": issue_id, "body": f"{BOT_PREFIX} {text}"},
        )

    def add_label(self, issue: Issue, name: str) -> None:
        nodes = self._post(
            "query($name: String!) { issueLabels(filter: { name: { eq: $name } }) {"
            " nodes { id team { id } } } }",
            {"name": name},
        )["issueLabels"]["nodes"]
        # Names repeat across teams, and a label of another team cannot go on this issue.
        usable = [n for n in nodes if n["team"] is None or n["team"]["id"] == issue.team_id]
        if not usable:
            raise LinearError(f"label {name!r} does not exist; create it in Linear (vm-setup.md)")
        self._post(
            "mutation($id: String!, $label: String!) {"
            " issueAddLabel(id: $id, labelId: $label) { success } }",
            {"id": issue.id, "label": usable[0]["id"]},
        )

    def comments_since(self, issue_id: str, after_iso: str | None) -> list[Comment]:
        data = self._post(
            """query($id: String!, $after: DateTimeOrDuration) { issue(id: $id) {
                 comments(first: 50, filter: { createdAt: { gt: $after } }) {
                   nodes { id body createdAt user { isMe } } } } }""",
            {"id": issue_id, "after": after_iso or "1970-01-01T00:00:00Z"},
        )
        nodes = data["issue"]["comments"]["nodes"]
        comments = [
            Comment(n["id"], n["body"], n["createdAt"], bool((n.get("user") or {}).get("isMe")))
            for n in nodes
        ]
        return sorted(comments, key=lambda c: c.created_at)

    def create_issue(self, team_id: str, title: str, description: str) -> str:
        data = self._post(
            "mutation($team: String!, $title: String!, $desc: String!) {"
            " issueCreate(input: { teamId: $team, title: $title, description: $desc })"
            " { issue { url } } }",
            {"team": team_id, "title": title, "desc": description},
        )
        return data["issueCreate"]["issue"]["url"]
