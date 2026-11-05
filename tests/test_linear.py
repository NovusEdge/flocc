import json
import urllib.error
from pathlib import Path

import pytest

from flocc.linear import BOT_PREFIX, Linear, LinearError, http_transport

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


def test_blocked_issues_reads_blocks_relations():
    node = {
        "id": "u2",
        "identifier": "NF-2",
        "title": "T",
        "description": "",
        "url": "u",
        "state": {"type": "unstarted"},
        "labels": {"nodes": [{"name": "web"}]},
        "project": None,
        "team": {"id": "t1"},
    }
    data = {
        "issue": {
            "relations": {
                "nodes": [
                    {"type": "blocks", "relatedIssue": node},
                    {"type": "related", "relatedIssue": {**node, "identifier": "NF-3"}},
                ]
            }
        }
    }
    linear = Linear(lambda q, v: data)
    assert [i.key for i in linear.blocked_issues("NF-1")] == ["NF-2"]


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


def test_comments_capture_author():
    post = FakePost(
        {
            "issue": {
                "comments": {
                    "nodes": [
                        {
                            "id": "c1",
                            "body": "a",
                            "createdAt": "2026-10-09T10:00:00Z",
                            "user": {"isMe": False, "id": "u-hermes"},
                        },
                        {
                            "id": "c2",
                            "body": "b",
                            "createdAt": "2026-10-09T10:05:00Z",
                            "user": None,
                            "botActor": {"id": "bot-hermes"},
                        },
                        {
                            "id": "c3",
                            "body": "c",
                            "createdAt": "2026-10-09T10:06:00Z",
                            "user": None,
                        },
                    ]
                }
            }
        }
    )
    authors = {c.id: c.author for c in Linear(post).comments_since("u1", None)}
    assert authors == {"c1": "u-hermes", "c2": "bot-hermes", "c3": None}


def test_missing_labels_counts_team_and_workspace_labels():
    post = FakePost(
        {
            "teams": {"nodes": [{"id": "t1"}]},
            "issueLabels": {
                "nodes": [
                    {"id": "l1", "name": "claude", "team": {"key": "NF"}},
                    {"id": "l2", "name": "escalation", "team": None},
                    {"id": "l3", "name": "backend", "team": {"key": "ORG"}},
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
        {"teams": team, "issueLabels": {"nodes": [{"id": "x", "name": "r0", "team": None}]}},
        {"teams": team, "issueLabels": {"nodes": [{"id": "x", "name": "r100", "team": None}]}},
    )
    names = [f"r{i}" for i in range(101)]
    missing = Linear(post).missing_labels("NF", names)
    assert [len(c[1]["names"]) for c in post.calls] == [100, 1]
    assert missing == names[1:100]


def test_missing_labels_on_an_unknown_team_raises():
    post = FakePost({"teams": {"nodes": []}, "issueLabels": {"nodes": []}})
    with pytest.raises(LinearError, match="team NF"):
        Linear(post).missing_labels("NF", ["claude"])


def detail_node(**over):
    node = {
        "id": "u1",
        "identifier": "NF-1",
        "title": "Fix rounding",
        "description": None,
        "url": "https://l/NF-1",
        "state": {"name": "In Progress"},
        "team": {"id": "t1"},
        "labels": {"nodes": [{"name": "claude"}]},
        "comments": {
            "nodes": [
                {"body": "b", "createdAt": "2026-11-02T10:00:00Z", "user": None, "botActor": None},
                {"body": "a", "createdAt": "2026-11-01T10:00:00Z", "user": {"name": "Owner"}},
            ]
        },
        "relations": {
            "nodes": [
                {
                    "type": "blocks",
                    "relatedIssue": {
                        "identifier": "NF-2",
                        "title": "T2",
                        "state": {"name": "Todo"},
                    },
                }
            ]
        },
        "inverseRelations": {
            "nodes": [
                {
                    "type": "related",
                    "issue": {"identifier": "NF-3", "title": "T3", "state": {"name": "Done"}},
                }
            ]
        },
    }
    return {**node, **over}


def test_issue_detail_reads_comments_oldest_first_and_both_relation_directions():
    d = Linear(FakePost({"issue": detail_node()})).issue_detail("NF-1")
    assert (d.key, d.description, d.state, d.team_id, d.labels) == (
        "NF-1",
        "",
        "In Progress",
        "t1",
        ("claude",),
    )
    assert d.comments == (
        ("2026-11-01T10:00:00Z", "Owner", "a"),
        ("2026-11-02T10:00:00Z", "unknown", "b"),
    )
    assert d.relations == (
        ("blocks", "NF-2", "Todo", "T2"),
        ("related (from)", "NF-3", "Done", "T3"),
    )


def test_search_filters_the_team_s_open_issues_by_title_or_description():
    post = FakePost(
        {
            "issues": {
                "nodes": [
                    {"identifier": "NF-5", "title": "Flaky export", "state": {"name": "Backlog"}}
                ]
            }
        }
    )
    assert Linear(post).search("NF", "export") == [("NF-5", "Backlog", "Flaky export")]
    assert post.calls[0][1] == {"team": "NF", "text": "export"}
    assert "completed" in post.calls[0][0] and "canceled" in post.calls[0][0]


def test_label_ids_keeps_team_and_workspace_labels_only():
    post = FakePost(
        {
            "teams": {"nodes": [{"id": "t1"}]},
            "issueLabels": {
                "nodes": [
                    {"id": "l1", "name": "cardinal-draft", "team": {"key": "NF"}},
                    {"id": "l2", "name": "other", "team": {"key": "ORG"}},
                ]
            },
        }
    )
    assert Linear(post).label_ids("NF", ["cardinal-draft", "other"]) == {"cardinal-draft": "l1"}


def test_state_id_takes_the_first_state_of_the_type():
    post = FakePost(
        {
            "team": {
                "states": {"nodes": [{"id": "b2", "position": 2.0}, {"id": "b1", "position": 1.0}]}
            }
        }
    )
    assert Linear(post).state_id("t1", "backlog") == "b1"
    assert post.calls[0][1] == {"team": "t1", "type": "backlog"}


def test_state_id_without_such_a_state_raises():
    post = FakePost({"team": {"states": {"nodes": []}}})
    with pytest.raises(LinearError, match="no backlog state"):
        Linear(post).state_id("t1", "backlog")


def test_create_issue_sends_the_labels_and_state_and_returns_the_new_key():
    post = FakePost(
        {
            "issueCreate": {
                "success": True,
                "issue": {"id": "u9", "identifier": "NF-9", "url": "https://l/NF-9"},
            }
        }
    )
    got = Linear(post).create_issue("t1", "Title", "Body", ["l1"], "b1")
    assert got == ("u9", "NF-9", "https://l/NF-9")
    assert post.calls[0][1] == {
        "team": "t1",
        "title": "Title",
        "description": "Body",
        "labels": ["l1"],
        "state": "b1",
    }


def test_create_issue_that_linear_refuses_raises():
    post = FakePost({"issueCreate": {"success": False, "issue": None}})
    with pytest.raises(LinearError, match="not created"):
        Linear(post).create_issue("t1", "Title", "Body", ["l1"], "b1")


def test_relate_links_the_two_issues_as_related():
    post = FakePost({"issueRelationCreate": {"success": True}})
    Linear(post).relate("u9", "u1")
    assert post.calls[0][1] == {"id": "u9", "related": "u1"}
    assert "type: related" in post.calls[0][0]


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
