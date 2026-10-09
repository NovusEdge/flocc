fmt:
    uv run ruff check --select I --fix
    uv run ruff format

lint:
    uv run ruff format --check
    uv run ruff check
    uv run pyrefly check
    shellcheck setup

test:
    uv run pytest -q

check: lint test
