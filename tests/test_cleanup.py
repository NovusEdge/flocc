import os

from flocc import ledger
from flocc.cleanup import NOW_SCRATCH_AGE_S, PYTEST_AGE_S, SCRATCH_AGE_S, STATE_AGE_S, cleanup
from tests.fakes import FakeAgent
from tests.test_ledger import make

NOW = 2_000_000_000.0


def test_cleanup_keeps_every_claim_and_removes_old_orphan_state(tmp_cfg):
    con = ledger.connect(tmp_cfg.ledger_path)
    ledger.insert_claim(con, make("NF-1"))
    ledger.insert_claim(con, make("NF-2", state="done"))
    for key in ("NF-1", "NF-2", "NF-3", "NF-4"):
        d = tmp_cfg.state_root / key
        d.mkdir(parents=True)
        os.utime(d, (NOW - 8 * 86400, NOW - 8 * 86400))
    os.utime(tmp_cfg.state_root / "NF-4", (NOW - 86400, NOW - 86400))
    (tmp_cfg.state_root / "not-a-key").mkdir()
    agent = FakeAgent(tmp_cfg)
    out = cleanup(tmp_cfg, con, agent, NOW)
    assert agent.swept == [(["NF-1", "NF-2"], NOW, STATE_AGE_S, SCRATCH_AGE_S, PYTEST_AGE_S)]
    assert out["removed"] == [str(tmp_cfg.state_root / "NF-3")]
    for name in ("NF-1", "NF-2", "NF-4", "not-a-key"):
        assert (tmp_cfg.state_root / name).exists()


def test_cleanup_now_passes_the_short_ages_and_the_bytes_freed(tmp_cfg):
    con = ledger.connect(tmp_cfg.ledger_path)
    agent = FakeAgent(tmp_cfg)
    out = cleanup(tmp_cfg, con, agent, NOW, scratch_age_s=NOW_SCRATCH_AGE_S, pytest_age_s=0.0)
    assert agent.swept == [([], NOW, STATE_AGE_S, NOW_SCRATCH_AGE_S, 0.0)]
    assert out == {"removed": [], "kept": [], "freed": 0}
