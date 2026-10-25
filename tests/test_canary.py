from flocc import canary, ledger


def con(tmp_path):
    return ledger.connect(tmp_path / "l.sqlite")


def test_a_mark_holds_while_main_stays_at_its_sha(tmp_path):
    c = con(tmp_path)
    canary.mark(c, "web", "abc123def456", 100.0, "x" * 2000 + "npm missing")
    m = canary.get_mark(c, "web")
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


def test_an_unreachable_remote_keeps_the_hold(tmp_path):
    c = con(tmp_path)
    canary.mark(c, "web", "abc", 100.0, "")
    assert canary.hold_reason(c, "web", "main", None, 200.0) is not None


def test_no_mark_or_a_torn_one_holds_nothing(tmp_path):
    c = con(tmp_path)
    assert canary.hold_reason(c, "web", "main", "abc", 1.0) is None
    ledger.meta_set(c, "main_broken:web", "{torn")
    assert canary.get_mark(c, "web") is None
