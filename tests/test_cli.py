from linear_agents.cli import main


def test_unknown_subcommand_exits_nonzero(capsys):
    assert main(["nope"]) == 2
    assert "invalid choice" in capsys.readouterr().err
