import json
import urllib.error
import urllib.request
from collections.abc import Callable
from dataclasses import dataclass

API = "https://api.linear.app/graphql"
BOT_PREFIX = "🤖 flocc:"
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
    # The Linear user id, or the bot/app actor id for an integration; None when neither is set.
    author: str | None = None


@dataclass(frozen=True)
class IssueDetail:
    id: str
    key: str
    title: str
    description: str
    url: str
    state: str
    team_id: str
    labels: tuple[str, ...]
    # (created_at, author name, body), oldest first.
    comments: tuple[tuple[str, str, str], ...]
    # (kind, key, state name, title); an inverse relation's kind ends in "(from)".
    relations: tuple[tuple[str, str, str, str], ...]


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

    def blocked_issues(self, key: str) -> list[Issue]:
        data = self._post(
            f"""query($id: String!) {{ issue(id: $id) {{ relations {{ nodes {{
                 type relatedIssue {{ {_ISSUE_FIELDS} }} }} }} }} }}""",
            {"id": key},
        )
        nodes = data["issue"]["relations"]["nodes"]
        return [_issue(n["relatedIssue"]) for n in nodes if n["type"] == "blocks"]

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

    def issue_detail(self, key: str) -> IssueDetail:
        related = "identifier title state { name }"
        data = self._post(
            f"""query($id: String!) {{ issue(id: $id) {{
                 id identifier title description url state {{ name }} team {{ id }}
                 labels {{ nodes {{ name }} }}
                 comments(first: 50) {{ nodes {{ body createdAt user {{ name }} botActor {{ name }} }} }}
                 relations {{ nodes {{ type relatedIssue {{ {related} }} }} }}
                 inverseRelations {{ nodes {{ type issue {{ {related} }} }} }} }} }}""",
            {"id": key},
        )["issue"]
        comments = sorted(
            (
                n["createdAt"],
                (n.get("user") or n.get("botActor") or {}).get("name") or "unknown",
                n["body"],
            )
            for n in data["comments"]["nodes"]
        )
        relations = [
            (n["type"], r["identifier"], r["state"]["name"], r["title"])
            for n in data["relations"]["nodes"]
            for r in [n["relatedIssue"]]
        ] + [
            (f"{n['type']} (from)", r["identifier"], r["state"]["name"], r["title"])
            for n in data["inverseRelations"]["nodes"]
            for r in [n["issue"]]
        ]
        return IssueDetail(
            id=data["id"],
            key=data["identifier"],
            title=data["title"],
            description=data.get("description") or "",
            url=data["url"],
            state=data["state"]["name"],
            team_id=data["team"]["id"],
            labels=tuple(n["name"] for n in data["labels"]["nodes"]),
            comments=tuple(comments),
            relations=tuple(relations),
        )

    def search(self, team_key: str, text: str) -> list[tuple[str, str, str]]:
        data = self._post(
            """query($team: String!, $text: String!) { issues(first: 20, filter: {
                 team: { key: { eq: $team } },
                 state: { type: { nin: ["completed", "canceled"] } },
                 or: [{ title: { containsIgnoreCase: $text } },
                      { description: { containsIgnoreCase: $text } }] }) {
                   nodes { identifier title state { name } } } }""",
            {"team": team_key, "text": text},
        )
        return [(n["identifier"], n["state"]["name"], n["title"]) for n in data["issues"]["nodes"]]

    def state_id(self, team_id: str, state_type: str) -> str:
        states = self._post(
            """query($team: String!, $type: String!) { team(id: $team) {
                 states(filter: { type: { eq: $type } }) { nodes { id position } } } }""",
            {"team": team_id, "type": state_type},
        )["team"]["states"]["nodes"]
        if not states:
            raise LinearError(f"team {team_id} has no {state_type} state")
        return min(states, key=lambda s: s["position"])["id"]

    def _move(self, issue: Issue, state_type: str) -> None:
        self._post(
            "mutation($id: String!, $stateId: String!) {"
            " issueUpdate(id: $id, input: { stateId: $stateId }) { success } }",
            {"id": issue.id, "stateId": self.state_id(issue.team_id, state_type)},
        )

    def create_issue(
        self, team_id: str, title: str, description: str, label_ids: list[str], state_id: str
    ) -> tuple[str, str, str]:
        made = self._post(
            """mutation($team: String!, $title: String!, $description: String!,
                        $labels: [String!], $state: String!) {
                 issueCreate(input: { teamId: $team, title: $title, description: $description,
                                      labelIds: $labels, stateId: $state }) {
                   success issue { id identifier url } } }""",
            {
                "team": team_id,
                "title": title,
                "description": description,
                "labels": label_ids,
                "state": state_id,
            },
        )["issueCreate"]
        if not made["success"] or not made["issue"]:
            raise LinearError(f"issue {title!r} was not created")
        issue = made["issue"]
        return issue["id"], issue["identifier"], issue["url"]

    def relate(self, issue_id: str, related_id: str) -> None:
        self._post(
            "mutation($id: String!, $related: String!) { issueRelationCreate(input: {"
            " issueId: $id, relatedIssueId: $related, type: related }) { success } }",
            {"id": issue_id, "related": related_id},
        )

    def comment(self, issue_id: str, text: str) -> None:
        self._post(
            "mutation($id: String!, $body: String!) {"
            " commentCreate(input: { issueId: $id, body: $body }) { success } }",
            {"id": issue_id, "body": f"{BOT_PREFIX} {text}"},
        )

    def label_ids(self, team_key: str, names: list[str]) -> dict[str, str]:
        found: dict[str, str] = {}
        # Chunked so a long name list cannot push a label past the 250-node page.
        for i in range(0, max(len(names), 1), 100):
            data = self._post(
                """query($key: String!, $names: [String!]) {
                     teams(filter: { key: { eq: $key } }) { nodes { id } }
                     issueLabels(first: 250, filter: { name: { in: $names } }) {
                       nodes { id name team { key } } } }""",
                {"key": team_key, "names": names[i : i + 100]},
            )
            if not data["teams"]["nodes"]:
                raise LinearError(f"team {team_key} does not exist or the key cannot see it")
            # A workspace label, or one of this team's.
            found |= {
                n["name"]: n["id"]
                for n in data["issueLabels"]["nodes"]
                if n["team"] is None or n["team"]["key"] == team_key
            }
        return found

    def missing_labels(self, team_key: str, names: list[str]) -> list[str]:
        usable = self.label_ids(team_key, names)
        return [n for n in names if n not in usable]

    def comments_since(self, issue_id: str, after_iso: str | None) -> list[Comment]:
        data = self._post(
            """query($id: String!, $after: DateTimeOrDuration) { issue(id: $id) {
                 comments(first: 50, filter: { createdAt: { gt: $after } }) {
                   nodes { id body createdAt user { isMe id } botActor { id } } } } }""",
            {"id": issue_id, "after": after_iso or "1970-01-01T00:00:00Z"},
        )
        nodes = data["issue"]["comments"]["nodes"]
        comments = []
        for n in nodes:
            user = n.get("user") or {}
            author = user.get("id") or (n.get("botActor") or {}).get("id")
            comments.append(
                Comment(n["id"], n["body"], n["createdAt"], bool(user.get("isMe")), author)
            )
        return sorted(comments, key=lambda c: c.created_at)
