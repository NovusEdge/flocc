import json
import urllib.error
from pathlib import Path

import pytest

from linear_agents.linear import BOT_PREFIX, Linear, LinearError, http_transport

FIX = Path(__file__).parent / "fixtures"


class FakePost:
    def __init__(self, *responses):
        self.responses = list(responses)
        self.calls = []

    def __call__(self, query, variables):
        self.calls.append((query, variables))
        return self.responses.pop(0)


def load(name):
    return json.loads((FIX / name).read_text())


def page2_issue():
    return Linear(FakePost(load("ready_page2.json"))).ready_issues("NF", "claude")[0]


def test_ready_issues_paginates():
    post = FakePost(load("ready_page1.json"), load("ready_page2.json"))
    issues = Linear(post).ready_issues("NF", "claude")
    assert [i.key for i in issues] == ["NF-1", "NF-2"]
    assert issues[0].labels == ("claude", "backend")
    assert issues[1].project == "backend" and issues[1].description == ""
    assert post.calls[1][1]["after"] == "c1"


def test_comment_is_prefixed():
    post = FakePost({"commentCreate": {"success": True}})
    Linear(post).comment("u1", "hello")
    assert post.calls[0][1]["body"] == f"{BOT_PREFIX} hello"


def test_start_picks_lowest_started_state():
    post = FakePost(
        {
            "team": {
                "states": {"nodes": [{"id": "s2", "position": 2.0}, {"id": "s1", "position": 1.0}]}
            }
        },
        {"issueUpdate": {"success": True}},
    )
    Linear(post).start(page2_issue())
    assert post.calls[0][1]["type"] == "started"
    assert post.calls[1][1] == {"id": "u2", "stateId": "s1"}


def test_unstart_moves_to_an_unstarted_state():
    post = FakePost(
        {"team": {"states": {"nodes": [{"id": "todo", "position": 0.0}]}}},
        {"issueUpdate": {"success": True}},
    )
    Linear(post).unstart(page2_issue())
    assert post.calls[0][1]["type"] == "unstarted"
    assert post.calls[1][1] == {"id": "u2", "stateId": "todo"}


def test_add_label_skips_other_teams_labels():
    post = FakePost(
        {
            "issueLabels": {
                "nodes": [
                    {"id": "theirs", "team": {"id": "t9"}},
                    {"id": "ours", "team": {"id": "t1"}},
                ]
            }
        },
        {"issueAddLabel": {"success": True}},
    )
    Linear(post).add_label(page2_issue(), "needs-decision")
    assert post.calls[1][1]["label"] == "ours"


def test_comments_mark_the_owners():
    post = FakePost(
        {
            "issue": {
                "comments": {
                    "nodes": [
                        {
                            "id": "c2",
                            "body": "b",
                            "createdAt": "2026-10-09T10:05:00Z",
                            "user": None,
                        },
                        {
                            "id": "c1",
                            "body": "a",
                            "createdAt": "2026-10-09T10:00:00Z",
                            "user": {"isMe": True},
                        },
                    ]
                }
            }
        }
    )
    comments = Linear(post).comments_since("u1", None)
    assert [(c.id, c.mine) for c in comments] == [("c1", True), ("c2", False)]


def test_missing_labels_counts_team_and_workspace_labels():
    post = FakePost(
        {
            "teams": {"nodes": [{"id": "t1"}]},
            "issueLabels": {
                "nodes": [
                    {"name": "claude", "team": {"key": "NF"}},
                    {"name": "escalation", "team": None},
                    {"name": "backend", "team": {"key": "ORG"}},
                ]
            },
        }
    )
    missing = Linear(post).missing_labels("NF", ["claude", "escalation", "backend"])
    assert missing == ["backend"]
    assert post.calls[0][1] == {"key": "NF", "names": ["claude", "escalation", "backend"]}


def test_missing_labels_queries_at_most_100_names_per_call():
    team = {"nodes": [{"id": "t1"}]}
    post = FakePost(
        {"teams": team, "issueLabels": {"nodes": [{"name": "r0", "team": None}]}},
        {"teams": team, "issueLabels": {"nodes": [{"name": "r100", "team": None}]}},
    )
    names = [f"r{i}" for i in range(101)]
    missing = Linear(post).missing_labels("NF", names)
    assert [len(c[1]["names"]) for c in post.calls] == [100, 1]
    assert missing == names[1:100]


def test_missing_labels_on_an_unknown_team_raises():
    post = FakePost({"teams": {"nodes": []}, "issueLabels": {"nodes": []}})
    with pytest.raises(LinearError, match="team NF"):
        Linear(post).missing_labels("NF", ["claude"])


def test_add_label_missing_label_raises():
    post = FakePost({"issueLabels": {"nodes": []}})
    with pytest.raises(LinearError, match="needs-decision"):
        Linear(post).add_label(page2_issue(), "needs-decision")


def test_http_transport_raises_on_graphql_errors(monkeypatch):
    class Resp:
        def __enter__(self):
            return self

        def __exit__(self, *a):
            return False

        def read(self):
            return json.dumps({"errors": [{"message": "RATELIMITED"}]}).encode()

    monkeypatch.setattr("urllib.request.urlopen", lambda req, timeout: Resp())
    with pytest.raises(LinearError, match="RATELIMITED"):
        http_transport("k")("query { viewer { id } }", {})


class Garbage:
    def __enter__(self):
        return self

    def __exit__(self, *a):
        return False

    def read(self):
        return b"<html>bad gateway</html>"


def refuse(req, timeout):
    raise urllib.error.URLError(ConnectionRefusedError(111, "Connection refused"))


def time_out(req, timeout):
    raise TimeoutError("timed out")


@pytest.mark.parametrize("urlopen", [refuse, time_out, lambda req, timeout: Garbage()])
def test_http_transport_wraps_network_failures(monkeypatch, urlopen):
    monkeypatch.setattr("urllib.request.urlopen", urlopen)
    with pytest.raises(LinearError):
        http_transport("k")("query { viewer { id } }", {})


def test_issue_by_key():
    node = load("ready_page2.json")["viewer"]["assignedIssues"]["nodes"][0]
    post = FakePost({"issue": node})
    assert Linear(post).issue("NF-2").key == "NF-2"
    assert post.calls[0][1] == {"id": "NF-2"}
