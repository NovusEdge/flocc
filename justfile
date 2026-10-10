[private]
default:
    @just --list --unsorted

# Format Python and Go sources in place
fmt:
    uv run ruff check --select I --fix
    uv run ruff format
    gofmt -w deploy installer

# Lint Python, the setup script and Go without changing files
lint:
    uv run ruff format --check
    uv run ruff check
    uv run pyrefly check
    shellcheck setup
    test -z "$(gofmt -l deploy installer)"
    go vet ./...

# Run the Python and Go test suites
test:
    uv run pytest -q
    go test ./...

# Lint, then test; what CI and pre-merge run
check: lint test

# Build la-setup for this machine and for linux amd64/arm64 hosts into build/
build-setup:
    mkdir -p build
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o build/la-setup-linux-amd64 ./installer
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o build/la-setup-linux-arm64 ./installer
    go build -o build/la-setup ./installer

# Set up or upgrade a host: an ssh destination (a Tailscale name) or daytona:<sandbox>
ship target *flags: build-setup
    build/la-setup ship {{flags}} {{target}}
