from flocc import canary, events, ledger


def con(tmp_path):
    return ledger.connect(tmp_path / "l.sqlite")


def test_a_mark_holds_while_main_stays_at_its_sha(tmp_path):
    c = con(tmp_path)
    canary.mark(c, "web", "abc123def456", 100.0, "x" * 2000 + "npm missing")
    m = canary.get_mark(c, "web")
    assert m is not None
    assert m["sha"] == "abc123def456" and len(m["output"]) == 1500
    reason = canary.hold_reason(c, "web", "main", "abc123def456", 200.0)
    assert reason == "./setup fails on main at abc123def; after fixing it, run flocc canary web"


def test_a_check_mark_names_the_check_and_clears_on_a_new_commit(tmp_path):
    c = con(tmp_path)
    canary.mark(c, "web", "abc123def456", 100.0, "E1 boom", check="just check")
    m = canary.get_mark(c, "web")
    assert m is not None and m["check"] == "just check"
    reason = canary.hold_reason(c, "web", "main", "abc123def456", 200.0)
    assert (
        reason == "`just check` fails on main at abc123def; after fixing it, run flocc canary web"
    )
    assert canary.hold_reason(c, "web", "main", "def", 200.0) is None
    assert canary.get_mark(c, "web") is None


def test_a_new_commit_on_main_clears_the_mark(tmp_path):
    c = con(tmp_path)
    canary.mark(c, "web", "abc", 100.0, "")
    assert canary.hold_reason(c, "web", "main", "def", 200.0) is None
    assert canary.get_mark(c, "web") is None


def test_a_mark_expires_after_a_day(tmp_path):
    c = con(tmp_path)
    canary.mark(c, "web", "abc", 100.0, "")
    assert canary.hold_reason(c, "web", "main", "abc", 100.0 + canary.HOLD_S) is None
    assert canary.get_mark(c, "web") is None


def test_a_break_is_one_base_broken_event_until_it_passes(tmp_path):
    c = con(tmp_path)
    canary.mark(c, "web", "abc123def456", 100.0, "boom", check="just check")
    canary.mark(c, "web", "abc123def456", 200.0, "boom", check="just check")
    assert canary.hold_reason(c, "web", "main", "abc123def456", 200.0 + canary.HOLD_S) is None
    canary.mark(c, "web", "abc123def456", 200.0 + canary.HOLD_S, "boom", check="just check")
    assert len(events.unacked(c)) == 1 and "abc123def" in events.unacked(c)[0].detail
    canary.mark(c, "web", "fff000fff000", 300.0, "boom", check="just check")
    canary.clear(c, "web")
    canary.mark(c, "web", "fff000fff000", 400.0, "boom", check="just check")
    canary.forget_all(c)
    canary.mark(c, "web", "fff000fff000", 500.0, "boom", check="just check")
    assert [(e.kind, e.repo) for e in events.unacked(c)] == [("base-broken", "web")] * 4


def test_an_unreachable_remote_keeps_the_hold(tmp_path):
    c = con(tmp_path)
    canary.mark(c, "web", "abc", 100.0, "")
    assert canary.hold_reason(c, "web", "main", None, 200.0) is not None


def test_no_mark_or_a_torn_one_holds_nothing(tmp_path):
    c = con(tmp_path)
    assert canary.hold_reason(c, "web", "main", "abc", 1.0) is None
    ledger.meta_set(c, "main_broken:web", "{torn")
    assert canary.get_mark(c, "web") is None


def test_a_base_broken_event_is_typed_repo(tmp_path):
    c = con(tmp_path)
    canary.mark(c, "web", "abc123def456", 100.0, "boom", check="just check")
    [e] = events.unacked(c)
    assert (e.kind, e.cause, e.stop_type) == ("base-broken", "base-broken", "repo")
    assert e.detail == "`just check` fails at abc123def"
