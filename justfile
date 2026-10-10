default:
    @just --list

fmt:
    uv run ruff check --select I --fix
    uv run ruff format
    gofmt -w deploy installer

lint:
    uv run ruff format --check
    uv run ruff check
    uv run pyrefly check
    shellcheck setup
    test -z "$(gofmt -l deploy installer)"
    go vet ./...

test:
    uv run pytest -q
    go test ./...

check: lint test

build-setup:
    mkdir -p build
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o build/la-setup-linux-amd64 ./installer
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o build/la-setup-linux-arm64 ./installer
    go build -o build/la-setup ./installer

# target: an ssh destination (a Tailscale name) or daytona:<sandbox>
ship target *flags: build-setup
    build/la-setup ship {{flags}} {{target}}
