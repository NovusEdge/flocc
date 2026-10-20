[private]
default:
    @just --list --unsorted

# Format Python and Go sources in place
[group('dev')]
fmt:
    uv run ruff check --select I --fix
    uv run ruff format
    gofmt -w deploy installer

# Lint Python, the setup script and Go without changing files
[group('dev')]
lint:
    uv run ruff format --check
    uv run ruff check
    uv run pyrefly check
    shellcheck setup
    test -z "$(gofmt -l deploy installer)"
    go vet ./...

# Run the Python and Go test suites
[group('dev')]
test: test-py test-go

# Run the Python tests
[group('dev')]
test-py:
    uv run pytest -q

# Run the Go tests
[group('dev')]
test-go:
    go test ./...

# Lint, then test; what CI and pre-merge run
[group('dev')]
check: lint test

# Build the desktop flocc command into build/
[group('build')]
build:
    mkdir -p build
    go build -o build/flocc ./installer/cmd/flocc

# Remove build/
[group('build')]
clean:
    rm -rf build

# Create a Daytona sandbox that never auto-stops
[group('daytona')]
daytona-up name:
    daytona create --snapshot daytona-medium --name {{name}} --auto-stop 0

# Stop a Daytona sandbox
[group('daytona')]
daytona-stop name:
    daytona stop {{name}}

# List Daytona sandboxes
[group('daytona')]
daytona-list:
    daytona list
