import json
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


def test_issue_by_key():
    node = load("ready_page2.json")["viewer"]["assignedIssues"]["nodes"][0]
    post = FakePost({"issue": node})
    assert Linear(post).issue("NF-2").key == "NF-2"
    assert post.calls[0][1] == {"id": "NF-2"}
