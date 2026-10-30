import os

import pytest

from flocc.agentfs import AgentFileError, entries, exists, mtime, read_text

as_root = pytest.mark.skipif(os.geteuid() == 0, reason="root may follow root-owned links")


def test_reads_a_file_and_misses_quietly(tmp_path):
    (tmp_path / "NF-1").mkdir()
    (tmp_path / "NF-1" / "done.json").write_text('{"summary": "ok"}')
    assert read_text(tmp_path / "NF-1", "done.json") == '{"summary": "ok"}'
    assert read_text(tmp_path / "NF-1", "idle") is None
    assert read_text(tmp_path / "NF-2", "done.json") is None
    assert exists(tmp_path / "NF-1", "done.json") and not exists(tmp_path / "NF-1", "idle")
    assert mtime(tmp_path / "NF-1", "done.json") is not None


@as_root
def test_a_link_planted_anywhere_on_the_path_is_refused(tmp_path):
    secret = tmp_path / "linear.key"
    secret.write_text("lin_api_secret")
    state = tmp_path / "agent" / "state"
    (state / "NF-1").mkdir(parents=True)
    (state / "NF-1" / "review.md").symlink_to(secret)
    with pytest.raises(AgentFileError):
        read_text(state / "NF-1", "review.md")
    (tmp_path / "done.json").write_text("{}")
    (state / "NF-2").symlink_to(tmp_path)
    with pytest.raises(AgentFileError):
        read_text(state / "NF-2", "done.json")
    with pytest.raises(AgentFileError):
        exists(state / "NF-2", "done.json")


def test_a_fifo_or_an_oversized_file_is_refused(tmp_path):
    os.mkfifo(tmp_path / "idle")
    with pytest.raises(AgentFileError):
        read_text(tmp_path, "idle")
    (tmp_path / "big").write_text("12345")
    with pytest.raises(AgentFileError):
        read_text(tmp_path, "big", limit=4)


@as_root
def test_a_directory_on_the_path_needs_search_but_not_read(tmp_path):
    # flocc-agent's home is 710: flocc-work may pass through it but not list it.
    home = tmp_path / "home"
    (home / "state" / "NF-1").mkdir(parents=True)
    (home / "state" / "NF-1" / "done.json").write_text("{}")
    home.chmod(0o300)
    try:
        assert read_text(home / "state" / "NF-1", "done.json") == "{}"
        assert exists(home / "state" / "NF-1", "done.json")
        assert entries(home / "state") == ["NF-1"]
    finally:
        home.chmod(0o700)


@as_root
def test_entries_lists_real_directories_only(tmp_path):
    (tmp_path / "NF-1").mkdir()
    (tmp_path / "NF-2").symlink_to(tmp_path / "NF-1")
    (tmp_path / "file").write_text("x")
    assert entries(tmp_path) == ["NF-1"]
    assert entries(tmp_path / "missing") == []
