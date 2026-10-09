import argparse
import sys


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="linear-agents")
    parser.add_argument("--config", default=None)
    parser.add_subparsers(dest="command", required=True)
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    try:
        args = parser.parse_args(argv)
    except SystemExit as exc:
        return int(exc.code or 0)
    print(f"{args.command}: not implemented", file=sys.stderr)
    return 1
