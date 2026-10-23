from dataclasses import replace

import pytest

from flocc import a2aclient, a2atick, inbox, ledger
from flocc.a2aconf import A2AConfig, NotConfigured, Peer
from flocc.context import state_dir
from tests.test_ledger import make
from tests.test_poller import issue, rt  # noqa: F401 - the fixture

A2A = A2AConfig(
    "h1",
    "t-h1",
    {},
    {
        "h1": Peer("h1", "http://h1:7443", ("backend",)),
        "h2": Peer("h2", "http://h2:7443", ("web",)),
    },
)


@pytest.fixture
def sent(monkeypatch):
    out = []

    def send(peer, token, **kw):
        out.append((peer.name, kw))
        return {"id": f"t{len(out)}", "status": {"state": "TASK_STATE_SUBMITTED"}}

    monkeypatch.setattr(a2aclient, "send", send)
    return out


def _claim(rt, key, state="running", repo="backend", pr=True):  # noqa: F811
    c = replace(make(key), repo=repo, state=state, phase="review" if pr else "impl")
    ledger.insert_claim(rt.con, c)
    d = state_dir(rt.cfg, key)
    d.mkdir(parents=True, exist_ok=True)
    if pr:
        (d / "pr-url").write_text(f"https://github.com/o/r/pull/{key[3:]}")
    return c


def _mail(rt, to="NF-9", body="x"):  # noqa: F811
    con = inbox.connect(inbox.db_path(rt.cfg.state_root))
    task = inbox.add(
        con,
        rt.cfg.state_root,
        to_issue=to,
        sender="owner",
        token_id="owner",
        kind="direct",
        body=body,
        meta={},
        now=1.0,
    )
    return con, task


def test_unclaimed_mail_waits_and_gets_one_record_comment(rt, sent):  # noqa: F811
    _mail(rt, body="first\nmore")
    rt.linear.state_types = lambda keys: {k: "unstarted" for k in keys}
    a2atick.step(rt, lambda: A2A)
    a2atick.step(rt, lambda: A2A)
    (comment,) = [t for _, t in rt.linear.comments]
    assert "owner" in comment and "direct" in comment and "first" in comment
    assert "more" not in comment


def test_closed_issues_settle(rt, sent):  # noqa: F811
    con, task = _mail(rt)
    rt.linear.state_types = lambda keys: {k: "canceled" for k in keys}
    a2atick.step(rt, lambda: A2A)
    msg = inbox.get(con, task)
    assert msg is not None and msg.state == "REJECTED"


def test_a_pr_opened_sends_handoffs_once(rt, sent):  # noqa: F811
    _claim(rt, "NF-1")
    rt.linear.blocked_issues = lambda key: [issue("NF-5", labels=("claude", "web"))]
    rt.pr_view = lambda claim, url: {"state": "OPEN", "files": [{"path": "api.py"}]}
    a2atick.step(rt, lambda: A2A)
    a2atick.step(rt, lambda: A2A)
    ((host, kw),) = sent
    assert host == "h2" and kw["to"] == "NF-5" and kw["kind"] == "handoff"
    assert kw["sender_as"] == "NF-1" and "api.py" in kw["body"]
    assert kw["extra"]["pr"].endswith("/pull/1")


def test_a_merge_sends_handoffs_and_repo_notices_once(rt, sent):  # noqa: F811
    _claim(rt, "NF-1", state="done")
    _claim(rt, "NF-2", pr=False)
    _claim(rt, "NF-3", repo="web", pr=False)
    ledger.meta_set(rt.con, "a2a_sent:NF-1:open", "1")
    rt.linear.blocked_issues = lambda key: [issue("NF-5", labels=("claude", "backend"))]
    rt.pr_view = lambda claim, url: {"state": "MERGED", "files": [{"path": "api.py"}]}
    a2atick.step(rt, lambda: A2A)
    a2atick.step(rt, lambda: A2A)
    got = sorted((kw["to"], kw["kind"]) for _, kw in sent)
    assert got == [("NF-2", "notice"), ("NF-5", "handoff")]


def test_failed_sends_stop_after_three_tries(rt, monkeypatch):  # noqa: F811
    _claim(rt, "NF-1")
    rt.linear.blocked_issues = lambda key: [issue("NF-5")]
    rt.pr_view = lambda claim, url: {"state": "OPEN", "files": []}
    calls = []

    def down(peer, token, **kw):
        calls.append(1)
        raise a2aclient.Unreachable("h1 did not answer")

    monkeypatch.setattr(a2aclient, "send", down)
    for _ in range(5):
        a2atick.step(rt, lambda: A2A)
    assert len(calls) == a2atick.RETRIES


def test_no_config_does_nothing(rt):  # noqa: F811
    def missing():
        raise NotConfigured("none")

    a2atick.step(rt, missing)
